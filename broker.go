package bites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"sync"
	"unsafe"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Broker carries a Topic's messages between the instances of an application (ADR 0081). A Topic made
// with NewTopic publishes through its Broker and delivers to its own Sockets only what the Broker
// hands back, so a message reaches the Sockets of every instance by one path, the publishing
// instance's included. The core ships no Broker; a module does (modules/pgnotify for Postgres
// LISTEN/NOTIFY). A Broker is implemented by modules and by tests, not called by an application:
// the server's own code hears a Topic with Topic.Listen, which brings MaxPayload, decoding, OnGap
// and the log with it.
//
// Every method must be safe for concurrent use. Subscribe and the function it returns must not
// block on the network, and the delivery functions may be called while the Broker holds no lock of
// its own that stop needs: a Topic calls stop from the goroutine of a closing Socket, and deliver
// may do so synchronously when a slow Socket is closed.
type Broker interface {
	// Accepts reports whether the Broker can carry a Topic of this name, so a name it cannot
	// carry (too long, a character it refuses) is refused by NewTopic and not at the first
	// Publish. A nil result means any Publish to this name is routed.
	Accepts(name string) error
	// Publish sends payload to every instance subscribed to name, this one included, and returns
	// once the Broker has accepted it, or the reason it did not. It must not truncate: a payload
	// over the Broker's own limit is a *PayloadError, which a program finds with errors.As to tell
	// "publish a reference instead" from a Broker that is down. Its Limit is the most bytes the
	// payload may have for this name, with whatever else the Broker counts (the name, a
	// separator) already taken off, so a program never computes it. Any other failure is any
	// other error.
	Publish(ctx context.Context, name string, payload []byte) error
	// Subscribe starts delivering to deliver every payload published to name, in the order the
	// Broker received them, and returns stop, which ends it. gap is called when the Broker may
	// have lost payloads (a connection that dropped and came back); it may be called from any
	// goroutine and must not block. Each payload given to deliver is deliver's to keep: the
	// Broker gives every call bytes of its own and never changes them afterwards. Subscribe never
	// fails: a Broker that is down delivers again when it is back.
	Subscribe(name string, deliver func(payload []byte), gap func()) (stop func())
}

// TopicOptions are the settings of a Topic made with NewTopic. The zero value is valid.
type TopicOptions struct {
	// OnGap is called on this instance when the Broker reports that payloads may have been lost
	// (for Postgres, the LISTEN connection dropped and came back), once the Topic is listening
	// again. The Sockets are not told anything by the framework: the application decides, usually
	// by calling PublishLocal with a message that tells clients to reload their state. It must
	// not block. Nil means a gap is logged at Warn and nothing else happens.
	OnGap func()
	// MaxPayload is the largest encoded message in bytes, checked on Publish and on what arrives.
	// Zero means 1 MiB, whatever the Broker accepts: a Broker with a lower limit of its own says so
	// in the *PayloadError of the Publish that goes over it.
	MaxPayload int
	// Log receives what happens to a message in silence otherwise: one that did not decode, one
	// over MaxPayload, a gap. Nil means slog.Default().
	Log *slog.Logger
}

const defaultMaxPayload = 1 << 20

// PayloadError is the error of a Publish whose message is larger than the limit that applies to
// it: a Topic's or a Topics' MaxPayload, or the limit of the Broker it publishes through (ADR 0081).
// It is returned as a *PayloadError, found with errors.As, so a program can tell a message that is
// too large, which it answers by publishing a reference and giving the Topic a Resolve, from a
// Broker that is down, which it retries or reports. Nothing was sent. The zero value is an error
// about a payload of 0 bytes, over a limit of 0, for no name; only a Publish makes one.
type PayloadError struct {
	// Name is the name the payload was published to: a Topic's, or a Topics' prefix and key.
	Name string
	// Size is the payload's size in bytes, as encoded.
	Size int
	// Limit is the most bytes the payload of this Name may have: the smaller of MaxPayload and,
	// when the Broker has a limit, what it leaves the payload for this Name (a Broker whose limit
	// counts the name and a separator has taken them off already). A payload of Limit bytes is
	// carried; one byte more is this error.
	Limit int
}

func (e *PayloadError) Error() string {
	return fmt.Sprintf("bites: Topic %q: the encoded message is %d bytes, over the limit of %d for this name: send less, or publish a reference and give the Topic a Resolve that reads the whole message on each instance", e.Name, e.Size, e.Limit)
}

// brokerLink is what a Topic made with NewTopic holds; the zero value of Topic has none.
type brokerLink struct {
	name    string
	broker  Broker
	max     int
	onGap   func()
	log     *slog.Logger
	deliver func([]byte)
	encode  func(v unsafe.Pointer) ([]byte, error) // v is a *Out

	mu   sync.Mutex // guards stop: the Topic listens while it has a subscriber
	stop func()
}

// NewTopic returns a Topic that publishes through broker under name and delivers to its Sockets
// what the broker receives from every instance. Every instance must use the same name and the same
// Out type. A Topic starts listening when its first Socket subscribes and stops when its last one
// leaves, so a Topic nobody is in costs the broker nothing and needs no Close. It panics with a
// RegistrationError, naming the line, when name is empty or the broker does not accept it, broker
// is nil, Out cannot be encoded as JSON (a field of a channel or function type), or MaxPayload is
// negative. The zero Topic[Out] is still the Topic of one process (ADR 0047).
func NewTopic[Out any](name string, broker Broker, o TopicOptions) *Topic[Out] {
	refuse := func(format string, args ...any) {
		label := "NewTopic"
		if name != "" {
			label += fmt.Sprintf(" %q", name)
		}
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + callerWhere()})
	}
	switch {
	case name == "":
		refuse("the name is empty: give the name every instance uses for this topic, such as \"room.7\"")
	case broker == nil:
		refuse("the broker is nil: give the Broker this topic publishes through, or use a zero bites.Topic[Out] for a topic of this process only")
	case o.MaxPayload < 0:
		refuse("MaxPayload is %d, which is not a limit: give a positive number of bytes, or zero for 1 MiB", o.MaxPayload)
	}
	if err := broker.Accepts(name); err != nil {
		refuse("the broker does not carry this name: %v", err)
	}
	// The payload is written and read by the framework's codec with options of its own, the same
	// on every instance whatever each app's naming, so an Opt, a Null and every type a Socket can
	// send cross the broker as they reach a socket. Unknown members are read past, so an instance
	// of the next release that added a field does not make this one drop its messages.
	pc, err := payloadCodecOf(reflect.TypeFor[Out]())
	if err != nil {
		refuse("%v", err)
	}
	return brokeredTopic[Out](name, broker, o, pc)
}

// brokeredTopic is the Topic NewTopic returns, once its arguments are known to be right; a
// Topics makes one for each key the same way.
func brokeredTopic[Out any](name string, broker Broker, o TopicOptions, pc *payloadCodec) *Topic[Out] {
	enc, dec := pc.enc, pc.dec
	l := &brokerLink{name: name, broker: broker, max: o.MaxPayload, onGap: o.OnGap, log: o.Log}
	l.encode = func(p unsafe.Pointer) ([]byte, error) { return enc.Append(nil, p) }
	if l.max == 0 {
		l.max = defaultMaxPayload
	}
	if l.log == nil {
		l.log = slog.Default()
	}
	t := &Topic[Out]{link: l}
	l.deliver = func(payload []byte) {
		if len(payload) > l.max {
			l.log.Warn("bites: a message was dropped, it is over MaxPayload", "topic", name, "bytes", len(payload), "max", l.max)
			return
		}
		var v Out
		if err := decodePayload(dec, payload, unsafe.Pointer(&v)); err != nil {
			l.log.Warn("bites: a message was dropped, it did not decode as the topic's type; every instance must use the same Out type", "topic", name, "bytes", len(payload), "err", err)
			return
		}
		if r := t.resolve.Load(); r != nil {
			var ok bool
			if v, ok = l.runResolve(*r, v); !ok {
				return
			}
		}
		if err := t.PublishLocal(v); err != nil {
			var so *jsoncodec.ServerOnlyError
			if errors.As(err, &so) {
				l.log.Warn("bites: a message holding a server-only variant was not sent to the sockets of this instance, which still pass it to Listen; give the Topic a Resolve that turns it into a variant a client is sent", "topic", name, "variant", so.Variant, "union", so.Union.String())
				return
			}
			l.log.Warn("bites: a message from the broker could not be encoded for the sockets", "topic", name, "err", err)
		}
	}
	return t
}

// payloadCodec is the plans a Topic's payload is written and read with, compiled once per Out
// type and shared by every Topic of it, so a Topic per room or per User compiles nothing.
type payloadCodec struct {
	enc *jsoncodec.Encoder
	dec *jsoncodec.Decoder
}

var payloadCodecs sync.Map // reflect.Type -> *payloadCodec

func payloadCodecOf(t reflect.Type) (*payloadCodec, error) {
	if pc, ok := payloadCodecs.Load(t); ok {
		return pc.(*payloadCodec), nil
	}
	copts := jsoncodec.Options{AllowUnknown: true, Undescribed: true, ServerVariants: true}
	enc, err := jsoncodec.CompileEncoder(t, copts)
	if err != nil {
		return nil, fmt.Errorf("the message type %s cannot be sent through a broker: %v: use a type a Socket can send, without channel or function fields", typeLabel(t), err)
	}
	dec, err := jsoncodec.CompileDecoder(t, copts)
	if err != nil {
		return nil, fmt.Errorf("the message type %s cannot be read back from a broker: %v", typeLabel(t), err)
	}
	pc, _ := payloadCodecs.LoadOrStore(t, &payloadCodec{enc: enc, dec: dec})
	return pc.(*payloadCodec), nil
}

// decodePayload decodes a broker's payload into dst. The strings of the value point into the
// payload, which the Broker gives each delivery to keep (its contract), so nothing is copied.
func decodePayload(dec *jsoncodec.Decoder, payload []byte, dst unsafe.Pointer) error {
	var slab wire.Slab
	slab.Adopt(payload, len(payload))
	var errs jsoncodec.Errors
	if err := dec.Decode(payload, dst, &slab, &errs); err != nil {
		return err
	}
	if len(errs.List) > 0 {
		e := errs.List[0]
		return fmt.Errorf("%s: %s", e.Location, e.Message)
	}
	return nil
}

// runResolve calls a Topic's Resolve on v, recovering a panic, which drops the message.
func (l *brokerLink) runResolve[Out any](fn func(Out) (Out, bool), v Out) (out Out, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("bites: Topic.Resolve panicked; the message was dropped on this instance", "topic", l.name, "panic", r)
			ok = false
		}
	}()
	return fn(v)
}

// Resolve gives a Topic made with NewTopic the function that each instance runs on every message
// the broker delivers, before its Sockets and the functions of Listen get it: they get what fn
// returns, and false drops the message on this instance. It is how a Topic carries a reference: a
// message too large for the broker (Postgres takes under 8000 bytes) is published as its id, and
// each instance reads the full message from its database in fn. fn runs on the broker's delivery
// goroutine, one message after another, so its work must be bounded (a query with a timeout); a
// panic in it is recovered, logged at Error with the topic's name, and drops that message. When Out
// is a tagged union (ADR 0083), the reference is best a variant its ServerOnly method names: it
// crosses the broker, fn turns it into a variant a client is sent, and a message that still holds
// it after fn, or on a Topic with no Resolve, is not sent to the Sockets (a Warn names the topic
// and the variant) while Listen functions get it. PublishLocal does not run it. Call it once, right after NewTopic: a message delivered before the
// call reaches the sockets as it arrived. A second call, a call on a zero Topic, which has no
// broker, and a nil fn panic.
func (t *Topic[Out]) Resolve(fn func(Out) (Out, bool)) {
	switch {
	case fn == nil:
		panic("bites: Topic.Resolve: fn is nil: give the function that turns a delivered message into the one the sockets get")
	case t.link == nil:
		panic("bites: Topic.Resolve on a Topic without a broker: a zero Topic delivers what this process publishes, which needs no resolving; make the Topic with NewTopic")
	}
	if !t.resolve.CompareAndSwap(nil, &fn) {
		panic("bites: Topic.Resolve called twice on topic " + strconv.Quote(t.link.name) + ": give one function")
	}
}

// publish encodes the Out at v and hands it to the broker.
func (l *brokerLink) publish(v unsafe.Pointer) error {
	return publishThrough(l.broker, l.name, l.max, l.encode, v)
}

// publishThrough encodes the Out at v with encode and hands it to broker under name, refusing
// one over max bytes. A Topic publishes through it, and a Topics for a key with no Topic here.
func publishThrough(broker Broker, name string, max int, encode func(unsafe.Pointer) ([]byte, error), v unsafe.Pointer) error {
	b, err := encode(v)
	if err != nil {
		return fmt.Errorf("bites: Topic %q: the message does not encode: %w", name, err)
	}
	if len(b) > max {
		return &PayloadError{Name: name, Size: len(b), Limit: max}
	}
	if err := broker.Publish(context.Background(), name, b); err != nil {
		var pe *PayloadError
		if errors.As(err, &pe) {
			return pe // the broker's own limit, said as it is, with nothing around it to read past
		}
		return fmt.Errorf("bites: Topic %q: publish through the broker: %w", name, err)
	}
	return nil
}

func (l *brokerLink) gap() {
	if l.onGap == nil {
		l.log.Warn("bites: the broker may have lost messages; set TopicOptions.OnGap to tell clients to reload", "topic", l.name)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("bites: TopicOptions.OnGap panicked", "topic", l.name, "panic", r)
		}
	}()
	l.onGap()
}

// sync starts or ends the broker subscription to match the number of local subscribers, counted
// under the link's lock so a join and a leave that race end in the right state.
func (l *brokerLink) sync(count func() int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := count()
	if n > 0 && l.stop == nil {
		l.stop = l.broker.Subscribe(l.name, l.deliver, l.gap)
	} else if n == 0 && l.stop != nil {
		l.stop()
		l.stop = nil
	}
}
