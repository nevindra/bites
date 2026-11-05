package ws

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unsafe"
)

var t0 = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

var testKey = [4]byte{0x37, 0xfa, 0x21, 0x3d}

// ev is an Event with its payload copied, so it survives the buffer being reused.
type ev struct {
	Kind    Kind
	Opcode  Opcode
	Payload string
	Start   bool
	End     bool
	Code    uint16
	Err     *Error
}

func newReader(lim Limits) *Reader {
	r := new(Reader)
	r.Reset(&lim)
	return r
}

// feed gives data to r in pieces of at most step bytes (the whole of it when step is 0) and
// returns every event that is not KindNeedMore. Each piece is a fresh copy, because Feed unmasks in place.
func feed(r *Reader, data []byte, step int, now time.Time) []ev {
	var out []ev
	if step <= 0 {
		step = len(data) + 1
	}
	for len(data) > 0 {
		piece := append([]byte(nil), data[:min(step, len(data))]...)
		data = data[len(piece):]
		for len(piece) > 0 {
			n, e := r.Feed(piece, now)
			piece = piece[n:]
			if e.Kind == KindNeedMore {
				if n == 0 {
					break
				}
				continue
			}
			out = append(out, ev{e.Kind, e.Opcode, string(e.Payload), e.Start, e.End, e.Code, e.Err})
			if e.Kind == KindFail {
				return out
			}
		}
	}
	return out
}

func frame(fin bool, op Opcode, payload string) []byte {
	return AppendClientFrame(nil, fin, op, []byte(payload), testKey)
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// message joins the chunks of consecutive KindData events into messages.
func messages(evs []ev) (msgs []string, ops []Opcode) {
	var cur strings.Builder
	for _, e := range evs {
		if e.Kind != KindData {
			continue
		}
		cur.WriteString(e.Payload)
		if e.End {
			msgs = append(msgs, cur.String())
			ops = append(ops, e.Opcode)
			cur.Reset()
		}
	}
	return
}

func wantFail(t *testing.T, evs []ev, code uint16, limit Limit) {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].Kind != KindFail {
		t.Fatalf("want a failure with %d, got events %+v", code, evs)
	}
	e := evs[len(evs)-1].Err
	if e.Code != code || e.Limit != limit {
		t.Fatalf("got close %d (%s: %s), want %d (limit %s)", e.Code, e.Limit, e.Reason, code, limit)
	}
}

func TestAMaskedTextFrameIsOneMessage(t *testing.T) {
	for _, step := range []int{0, 1, 2, 3, 5} {
		r := newReader(DefaultLimits())
		evs := feed(r, frame(true, OpText, "hello"), step, t0)
		msgs, ops := messages(evs)
		if len(msgs) != 1 || msgs[0] != "hello" || ops[0] != OpText {
			t.Fatalf("step %d: got %+v", step, evs)
		}
		if !evs[0].Start || !evs[len(evs)-1].End {
			t.Fatalf("step %d: Start/End flags wrong in %+v", step, evs)
		}
	}
}

func TestPayloadIsUnmaskedInPlaceInTheCallersSlice(t *testing.T) {
	r := newReader(DefaultLimits())
	in := frame(true, OpBinary, "abcdefghijk")
	n, e := r.Feed(in, t0)
	if n != len(in) || e.Kind != KindData || string(e.Payload) != "abcdefghijk" {
		t.Fatalf("got n=%d %+v", n, e)
	}
	if &e.Payload[0] != &in[len(in)-11] {
		t.Fatal("the payload is a copy, not a view of the input")
	}
}

func TestAnUnmaskedClientFrameClosesWith1002(t *testing.T) {
	r := newReader(DefaultLimits())
	evs := feed(r, AppendFrame(nil, OpText, []byte("hi")), 0, t0)
	wantFail(t, evs, 1002, LimitNone)
	if evs[0].Err != ErrUnmasked {
		t.Fatalf("got %v", evs[0].Err)
	}
}

func TestAnUnmaskedFrameIsRefusedAfterTwoBytes(t *testing.T) {
	r := newReader(DefaultLimits())
	n, e := r.Feed([]byte{0x81, 0x05, 'h'}, t0)
	if e.Kind != KindFail || n != 2 {
		t.Fatalf("n=%d %+v", n, e)
	}
}

func TestAReservedBitSetClosesWith1002(t *testing.T) {
	for _, bit := range []byte{0x40, 0x20, 0x10} {
		f := frame(true, OpText, "x")
		f[0] |= bit
		wantFail(t, feed(newReader(DefaultLimits()), f, 0, t0), 1002, LimitNone)
	}
}

func TestAReservedOpcodeClosesWith1002(t *testing.T) {
	for _, op := range []Opcode{3, 4, 5, 6, 7, 0xB, 0xC, 0xD, 0xE, 0xF} {
		f := frame(true, op, "")
		wantFail(t, feed(newReader(DefaultLimits()), f, 0, t0), 1002, LimitNone)
	}
}

func TestANonMinimalLengthEncodingClosesWith1002(t *testing.T) {
	// 16-bit form carrying 5, 64-bit form carrying 5 and carrying 65535.
	cases := map[string][]byte{
		"16-bit form of 5":     {0x82, 0x80 | 126, 0, 5, 1, 2, 3, 4, 0, 0, 0, 0, 0},
		"16-bit form of 125":   {0x82, 0x80 | 126, 0, 125, 1, 2, 3, 4},
		"64-bit form of 5":     {0x82, 0x80 | 127, 0, 0, 0, 0, 0, 0, 0, 5, 1, 2, 3, 4, 0, 0, 0, 0, 0},
		"64-bit form of 65535": {0x82, 0x80 | 127, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 1, 2, 3, 4},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			wantFail(t, feed(newReader(DefaultLimits()), f, 0, t0), 1002, LimitNone)
		})
	}
}

func TestTheSmallestLengthsOfTheLongFormsAreAccepted(t *testing.T) {
	for _, n := range []int{125, 126, 65535, 65536} {
		r := newReader(Limits{MaxMessage: 1 << 20, MaxFragments: 1, MaxControl: 125, PingsPerSecond: 1, CloseTimeout: time.Second})
		msgs, _ := messages(feed(r, frame(true, OpBinary, strings.Repeat("a", n)), 1500, t0))
		if len(msgs) != 1 || len(msgs[0]) != n {
			t.Fatalf("length %d: got %d messages", n, len(msgs))
		}
	}
}

func TestALengthWithItsHighBitSetClosesWith1002(t *testing.T) {
	f := []byte{0x82, 0x80 | 127, 0x80, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
	evs := feed(newReader(DefaultLimits()), f, 0, t0)
	wantFail(t, evs, 1002, LimitNone)
	if evs[0].Err != ErrLengthMSB {
		t.Fatalf("got %v", evs[0].Err)
	}
}

func TestAControlFrameOver125BytesClosesWith1002(t *testing.T) {
	for _, op := range []Opcode{OpPing, OpPong, OpClose} {
		f := frame(true, op, strings.Repeat("a", 126))
		n, e := newReader(DefaultLimits()).Feed(f, t0)
		if e.Kind != KindFail || e.Err.Code != 1002 || e.Err.Limit != LimitControl || n != 2 {
			t.Fatalf("op %d: n=%d %+v", op, n, e)
		}
	}
	// 125 is the largest allowed.
	evs := feed(newReader(DefaultLimits()), frame(true, OpPing, strings.Repeat("a", 125)), 0, t0)
	if len(evs) != 1 || evs[0].Kind != KindPing || len(evs[0].Payload) != 125 {
		t.Fatalf("got %+v", evs)
	}
}

func TestAControlFrameLimitBelow125IsHonoured(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxControl = 10
	wantFail(t, feed(newReader(lim), frame(true, OpPing, strings.Repeat("a", 11)), 0, t0), 1002, LimitControl)
	if evs := feed(newReader(lim), frame(true, OpPing, strings.Repeat("a", 10)), 0, t0); evs[0].Kind != KindPing {
		t.Fatalf("got %+v", evs)
	}
}

func TestAFragmentedControlFrameClosesWith1002(t *testing.T) {
	for _, op := range []Opcode{OpPing, OpPong, OpClose} {
		wantFail(t, feed(newReader(DefaultLimits()), frame(false, op, "x"), 0, t0), 1002, LimitControl)
	}
}

func TestAContinuationWithNothingToContinueClosesWith1002(t *testing.T) {
	r := newReader(DefaultLimits())
	wantFail(t, feed(r, frame(true, OpContinuation, "x"), 0, t0), 1002, LimitNone)
	// Also after a finished message.
	r = newReader(DefaultLimits())
	wantFail(t, feed(r, cat(frame(true, OpText, "a"), frame(true, OpContinuation, "x")), 0, t0), 1002, LimitNone)
}

func TestANewDataFrameInsideAFragmentedMessageClosesWith1002(t *testing.T) {
	r := newReader(DefaultLimits())
	evs := feed(r, cat(frame(false, OpText, "a"), frame(true, OpText, "b")), 0, t0)
	wantFail(t, evs, 1002, LimitNone)
}

func TestAControlFrameMayInterruptAFragmentedMessage(t *testing.T) {
	r := newReader(DefaultLimits())
	evs := feed(r, cat(frame(false, OpText, "he"), frame(true, OpPing, "p"), frame(false, OpContinuation, "ll"), frame(true, OpPong, ""), frame(true, OpContinuation, "o")), 3, t0)
	msgs, _ := messages(evs)
	if len(msgs) != 1 || msgs[0] != "hello" {
		t.Fatalf("got %+v", evs)
	}
	var kinds []Kind
	for _, e := range evs {
		if e.Kind != KindData {
			kinds = append(kinds, e.Kind)
		}
	}
	if len(kinds) != 2 || kinds[0] != KindPing || kinds[1] != KindPong {
		t.Fatalf("got control events %v", kinds)
	}
}

func TestAnEmptyFinalFragmentEndsTheMessage(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), cat(frame(false, OpBinary, "ab"), frame(true, OpContinuation, "")), 0, t0)
	if len(evs) != 2 || !evs[0].Start || evs[0].End || evs[1].Payload != "" || !evs[1].End || evs[1].Start {
		t.Fatalf("got %+v", evs)
	}
}

func TestAnEmptyMessageIsOneEventWithStartAndEnd(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), frame(true, OpText, ""), 0, t0)
	if len(evs) != 1 || !evs[0].Start || !evs[0].End || evs[0].Payload != "" {
		t.Fatalf("got %+v", evs)
	}
}

func TestBinaryFramesAreReportedWithTheirOpcode(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), frame(true, OpBinary, "\xff\xfe"), 0, t0)
	if len(evs) != 1 || evs[0].Opcode != OpBinary || evs[0].Payload != "\xff\xfe" {
		t.Fatalf("got %+v", evs)
	}
}

func TestAMessageOver64KiBClosesWith1009BeforeItsPayloadIsRead(t *testing.T) {
	f := frame(true, OpBinary, strings.Repeat("a", 64<<10+1))
	r := newReader(DefaultLimits())
	n, e := r.Feed(f, t0)
	if e.Kind != KindFail || e.Err.Code != 1009 || e.Err.Limit != LimitMessage {
		t.Fatalf("got %+v", e)
	}
	if n != 2+8+4 {
		t.Fatalf("read %d bytes, want the 14 of the header only", n)
	}
	// Exactly 64 KiB is allowed.
	msgs, _ := messages(feed(newReader(DefaultLimits()), frame(true, OpBinary, strings.Repeat("a", 64<<10)), 4096, t0))
	if len(msgs) != 1 || len(msgs[0]) != 64<<10 {
		t.Fatal("a message of exactly the limit was refused")
	}
}

func TestAMessageOver64KiBAcrossFragmentsClosesWith1009AtTheFragmentThatCrossesIt(t *testing.T) {
	a := strings.Repeat("a", 40<<10)
	evs := feed(newReader(DefaultLimits()), cat(frame(false, OpBinary, a), frame(true, OpContinuation, a)), 4096, t0)
	wantFail(t, evs, 1009, LimitMessage)
	var got int
	for _, e := range evs {
		got += len(e.Payload)
	}
	if got != 40<<10 {
		t.Fatalf("delivered %d bytes before the refusal, want %d", got, 40<<10)
	}
}

func TestTheMessageLimitIsPerMessageNotPerConnection(t *testing.T) {
	r := newReader(DefaultLimits())
	m := frame(true, OpBinary, strings.Repeat("a", 40<<10))
	msgs, _ := messages(feed(r, cat(m, m, m), 0, t0))
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
}

func TestA129thFragmentClosesWith1009(t *testing.T) {
	var b [][]byte
	b = append(b, frame(false, OpText, "x"))
	for i := 0; i < 126; i++ {
		b = append(b, frame(false, OpContinuation, "x"))
	}
	// 127 fragments so far, 128 with this one, which is allowed.
	b = append(b, frame(true, OpContinuation, "x"))
	msgs, _ := messages(feed(newReader(DefaultLimits()), cat(b...), 0, t0))
	if len(msgs) != 1 || len(msgs[0]) != 128 {
		t.Fatalf("128 fragments were refused: %d messages", len(msgs))
	}
	b = b[:len(b)-1]
	b = append(b, frame(false, OpContinuation, "x"), frame(true, OpContinuation, "x"))
	wantFail(t, feed(newReader(DefaultLimits()), cat(b...), 0, t0), 1009, LimitFragments)
}

func TestTheFragmentCountStartsOverForTheNextMessage(t *testing.T) {
	var b [][]byte
	for m := 0; m < 3; m++ {
		b = append(b, frame(false, OpText, "x"))
		for i := 0; i < 126; i++ {
			b = append(b, frame(false, OpContinuation, "x"))
		}
		b = append(b, frame(true, OpContinuation, "x"))
	}
	msgs, _ := messages(feed(newReader(DefaultLimits()), cat(b...), 0, t0))
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
}

func TestATextMessageWithBadUTF8Closes1007(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), frame(true, OpText, "ok\xff"), 0, t0)
	wantFail(t, evs, 1007, LimitNone)
}

func TestATextMessageWithBadUTF8SplitAcrossTwoFragmentsCloses1007(t *testing.T) {
	// "\xe2\x82" starts a 3-byte rune; "x" cannot continue it.
	for _, step := range []int{0, 1} {
		evs := feed(newReader(DefaultLimits()), cat(frame(false, OpText, "a\xe2\x82"), frame(true, OpContinuation, "x")), step, t0)
		wantFail(t, evs, 1007, LimitNone)
	}
}

func TestATextMessageEndingInTheMiddleOfARuneCloses1007(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), frame(true, OpText, "a\xe2\x82"), 0, t0)
	wantFail(t, evs, 1007, LimitNone)
	evs = feed(newReader(DefaultLimits()), cat(frame(false, OpText, "a\xe2\x82"), frame(true, OpContinuation, "")), 0, t0)
	wantFail(t, evs, 1007, LimitNone)
}

func TestARuneSplitAcrossFragmentsAndReadsIsAccepted(t *testing.T) {
	text := "héllo € \U0001F600 end"
	for cut := 0; cut <= len(text); cut++ {
		for _, step := range []int{0, 1, 2} {
			in := cat(frame(false, OpText, text[:cut]), frame(true, OpContinuation, text[cut:]))
			msgs, _ := messages(feed(newReader(DefaultLimits()), in, step, t0))
			if len(msgs) != 1 || msgs[0] != text {
				t.Fatalf("cut %d step %d: got %q", cut, step, msgs)
			}
		}
	}
}

func TestOverlongAndSurrogateAndTooLargeRunesClose1007(t *testing.T) {
	for _, bad := range []string{"\xc0\x80", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xf8\x88\x80\x80\x80", "\x80", "\xe0\x80\x80"} {
		wantFail(t, feed(newReader(DefaultLimits()), frame(true, OpText, bad), 0, t0), 1007, LimitNone)
	}
}

func TestBadUTF8IsRefusedWithoutWaitingForTheEndOfTheMessage(t *testing.T) {
	f := cat(frame(false, OpText, "\xc0"), frame(false, OpContinuation, "tail"))
	evs := feed(newReader(DefaultLimits()), f, 0, t0)
	wantFail(t, evs, 1007, LimitNone)
	if len(evs) != 1 {
		t.Fatalf("got %+v", evs)
	}
}

func TestBinaryMessagesAreNotCheckedForUTF8(t *testing.T) {
	msgs, _ := messages(feed(newReader(DefaultLimits()), cat(frame(false, OpBinary, "\xc0"), frame(true, OpContinuation, "\xe2")), 0, t0))
	if len(msgs) != 1 {
		t.Fatal("binary was refused")
	}
}

func TestAPingFloodOver32ASecondClosesWith1008(t *testing.T) {
	var b [][]byte
	for i := 0; i < 33; i++ {
		b = append(b, frame(true, OpPing, ""))
	}
	evs := feed(newReader(DefaultLimits()), cat(b...), 0, t0)
	wantFail(t, evs, 1008, LimitPingRate)
	if len(evs) != 33 || evs[31].Kind != KindPing {
		t.Fatalf("want 32 pings then the refusal, got %d events", len(evs))
	}
}

func TestThirtyTwoPingsInASecondAreAccepted(t *testing.T) {
	var b [][]byte
	for i := 0; i < 32; i++ {
		b = append(b, frame(true, OpPing, ""))
	}
	evs := feed(newReader(DefaultLimits()), cat(b...), 0, t0)
	if len(evs) != 32 || evs[31].Kind != KindPing {
		t.Fatalf("got %d events, last %+v", len(evs), evs[len(evs)-1])
	}
}

func TestPingsPacedAt32ASecondAreAcceptedForever(t *testing.T) {
	r := newReader(DefaultLimits())
	now := t0
	for i := 0; i < 1000; i++ {
		evs := feed(r, frame(true, OpPing, ""), 0, now)
		if len(evs) != 1 || evs[0].Kind != KindPing {
			t.Fatalf("ping %d: %+v", i, evs)
		}
		now = now.Add(time.Second / 32)
	}
}

func TestAPingBurstIsAllowedAgainAfterASecondOfQuiet(t *testing.T) {
	r := newReader(DefaultLimits())
	ping := frame(true, OpPing, "")
	for round := 0; round < 3; round++ {
		now := t0.Add(time.Duration(round) * time.Second)
		for i := 0; i < 32; i++ {
			if evs := feed(r, ping, 0, now); evs[0].Kind != KindPing {
				t.Fatalf("round %d ping %d refused", round, i)
			}
		}
	}
	wantFail(t, feed(r, ping, 0, t0.Add(2*time.Second)), 1008, LimitPingRate)
}

func TestThePingRateIsConfigurable(t *testing.T) {
	lim := DefaultLimits()
	lim.PingsPerSecond = 2
	evs := feed(newReader(lim), cat(frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, "")), 0, t0)
	wantFail(t, evs, 1008, LimitPingRate)
}

func TestPongsAndPingsDrainOneBucketAndThe33rdAtOneInstantFailsWith1008(t *testing.T) {
	var b [][]byte
	for i := 0; i < 33; i++ {
		b = append(b, frame(true, OpPong, ""))
	}
	evs := feed(newReader(DefaultLimits()), cat(b...), 0, t0)
	if len(evs) != 33 || evs[31].Kind != KindPong {
		t.Fatalf("got %d events", len(evs))
	}
	wantFail(t, evs, 1008, LimitPingRate)
	mixed := cat(b[:16]...)
	mixed = append(mixed, cat(frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""),
		frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""),
		frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPing, ""),
		frame(true, OpPing, ""), frame(true, OpPing, ""), frame(true, OpPong, ""))...)
	wantFail(t, feed(newReader(DefaultLimits()), mixed, 0, t0), 1008, LimitPingRate)
}

func TestAPongAtTheSteadyRateIsAcceptedForever(t *testing.T) {
	r := newReader(DefaultLimits())
	now := t0
	for i := 0; i < 3200; i++ {
		evs := feed(r, frame(true, OpPong, ""), 0, now)
		if len(evs) != 1 || evs[0].Kind != KindPong {
			t.Fatalf("pong %d: %+v", i, evs)
		}
		now = now.Add(time.Second / 32)
	}
}

func TestAClientsEchoOfTheStatusTheServerClosedWithIsAcceptedAndNoOtherUnpromptedServerStatusIs(t *testing.T) {
	for _, code := range []uint16{1012, 1013, 1014} {
		r := newReader(DefaultLimits())
		wantFail(t, feed(r, frame(true, OpClose, closePayload(code, "")), 0, t0), 1002, LimitNone)
		r = newReader(DefaultLimits())
		r.AllowEcho(code)
		evs := feed(r, frame(true, OpClose, closePayload(code, "")), 0, t0)
		if len(evs) != 1 || evs[0].Kind != KindClose || evs[0].Code != code {
			t.Fatalf("echo of %d: %+v", code, evs)
		}
		r = newReader(DefaultLimits())
		r.AllowEcho(code)
		wantFail(t, feed(r, frame(true, OpClose, closePayload(1006, "")), 0, t0), 1002, LimitNone)
	}
}

func TestAPingSplitAcrossReadsIsReportedWhole(t *testing.T) {
	in := cat(frame(true, OpPing, "0123456789"), frame(true, OpText, "after"))
	for _, step := range []int{1, 3, 7} {
		evs := feed(newReader(DefaultLimits()), in, step, t0)
		if len(evs) < 2 || evs[0].Kind != KindPing || evs[0].Payload != "0123456789" {
			t.Fatalf("step %d: %+v", step, evs)
		}
		if msgs, _ := messages(evs); len(msgs) != 1 || msgs[0] != "after" {
			t.Fatalf("step %d: %+v", step, evs)
		}
	}
}

func TestACloseWithNoPayloadReportsNoStatus(t *testing.T) {
	evs := feed(newReader(DefaultLimits()), frame(true, OpClose, ""), 0, t0)
	if len(evs) != 1 || evs[0].Kind != KindClose || evs[0].Code != CloseNoStatus {
		t.Fatalf("got %+v", evs)
	}
}

func closePayload(code uint16, reason string) string {
	return string([]byte{byte(code >> 8), byte(code)}) + reason
}

func TestACloseWithAStatusAndReasonIsReported(t *testing.T) {
	for _, code := range []uint16{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 3000, 3999, 4000, 4999} {
		evs := feed(newReader(DefaultLimits()), frame(true, OpClose, closePayload(code, "bye é")), 1, t0)
		if len(evs) != 1 || evs[0].Kind != KindClose || evs[0].Code != code || evs[0].Payload != "bye é" {
			t.Fatalf("code %d: %+v", code, evs)
		}
	}
}

func TestACloseStatusThatMayNotAppearOnTheWireClosesWith1002(t *testing.T) {
	for _, code := range []uint16{0, 999, 1004, 1005, 1006, 1012, 1013, 1014, 1015, 1016, 1100, 2000, 2999, 5000, 65535} {
		evs := feed(newReader(DefaultLimits()), frame(true, OpClose, closePayload(code, "")), 0, t0)
		wantFail(t, evs, 1002, LimitNone)
	}
}

func TestACloseFrameOfOneByteClosesWith1002(t *testing.T) {
	wantFail(t, feed(newReader(DefaultLimits()), frame(true, OpClose, "x"), 0, t0), 1002, LimitNone)
}

func TestACloseReasonThatIsNotUTF8Closes1007(t *testing.T) {
	wantFail(t, feed(newReader(DefaultLimits()), frame(true, OpClose, closePayload(1000, "\xff")), 0, t0), 1007, LimitNone)
}

func TestInputAfterACloseFrameIsDiscarded(t *testing.T) {
	r := newReader(DefaultLimits())
	in := cat(frame(true, OpClose, closePayload(1000, "")), []byte{0xff, 0xff, 0xff})
	n, e := r.Feed(in, t0)
	if e.Kind != KindClose || n != len(in)-3 {
		t.Fatalf("n=%d %+v", n, e)
	}
	n, e = r.Feed(in[n:], t0)
	if e.Kind != KindNeedMore || n != 3 {
		t.Fatalf("after close: n=%d %+v", n, e)
	}
}

func TestAFailureIsRepeatedAndNothingMoreIsRead(t *testing.T) {
	r := newReader(DefaultLimits())
	if _, e := r.Feed([]byte{0x81, 0x01}, t0); e.Err != ErrUnmasked {
		t.Fatalf("got %+v", e)
	}
	n, e := r.Feed(frame(true, OpText, "fine"), t0)
	if n != 0 || e.Kind != KindFail || e.Err != ErrUnmasked {
		t.Fatalf("n=%d %+v", n, e)
	}
}

func TestMasksAtEveryOffsetAndLengthRoundTripWhateverTheReadSizes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	lim := Limits{MaxMessage: 1 << 20, MaxFragments: 1000, MaxControl: 125, PingsPerSecond: 1000, CloseTimeout: time.Second}
	for i := 0; i < 300; i++ {
		payload := make([]byte, rng.Intn(400))
		rng.Read(payload)
		var key [4]byte
		rng.Read(key[:])
		in := AppendClientFrame(nil, true, OpBinary, payload, key)
		evs := feed(newReader(lim), in, 1+rng.Intn(40), t0)
		msgs, _ := messages(evs)
		if len(msgs) != 1 || msgs[0] != string(payload) {
			t.Fatalf("iteration %d: payload of %d bytes did not round trip", i, len(payload))
		}
	}
}

func TestResetRefusesALimitThatIsNotPositive(t *testing.T) {
	for _, mut := range []func(*Limits){
		func(l *Limits) { l.MaxMessage = 0 },
		func(l *Limits) { l.MaxFragments = 0 },
		func(l *Limits) { l.MaxControl = 126 },
		func(l *Limits) { l.PingsPerSecond = 0 },
	} {
		lim := DefaultLimits()
		mut(&lim)
		func() {
			defer func() {
				if recover() == nil {
					t.Error("Reset accepted a bad limit")
				}
			}()
			new(Reader).Reset(&lim)
		}()
	}
}

func TestTheDefaultLimitsAreThoseOfADR0046(t *testing.T) {
	l := DefaultLimits()
	if l.MaxMessage != 64<<10 || l.MaxFragments != 128 || l.MaxControl != 125 || l.PingsPerSecond != 32 || l.CloseTimeout != 5*time.Second {
		t.Fatalf("got %+v", l)
	}
}

func TestAReaderStaysSmall(t *testing.T) {
	// A Reader is held for every idle socket (ADR 0003). Raise this only knowingly.
	if s := unsafe.Sizeof(Reader{}); s > 192 {
		t.Fatalf("Reader is %d bytes", s)
	}
}

func TestReadingAFrameOnTheSteadyPathAllocatesNothing(t *testing.T) {
	r := newReader(DefaultLimits())
	text := frame(true, OpText, strings.Repeat("a", 100))
	ping := frame(true, OpPing, "ping")
	frag1 := frame(false, OpText, "abc")
	frag2 := frame(true, OpContinuation, "def")
	buf := make([]byte, 0, 512)
	now := t0
	run := func(f []byte) {
		rest := append(buf[:0], f...)
		now = now.Add(time.Second)
		for len(rest) > 0 {
			n, _ := r.Feed(rest, now)
			if n == 0 {
				t.Fatal("Feed made no progress")
			}
			rest = rest[n:]
		}
	}
	for name, f := range map[string][]byte{"text": text, "ping": ping, "fragments": cat(frag1, frag2)} {
		if a := testing.AllocsPerRun(200, func() { run(f) }); a != 0 {
			t.Errorf("%s frame: %v allocations", name, a)
		}
	}
}

func TestAControlFrameSplitAcrossReadsAllocatesOnlyTheFirstTime(t *testing.T) {
	r := newReader(DefaultLimits())
	ping := frame(true, OpPing, "0123456789")
	buf := make([]byte, 0, 64)
	now := t0
	run := func() {
		now = now.Add(time.Second)
		buf = append(buf[:0], ping...)
		for _, piece := range [][]byte{buf[:5], buf[5:]} {
			for len(piece) > 0 {
				n, _ := r.Feed(piece, now)
				piece = piece[n:]
			}
		}
	}
	run()
	if a := testing.AllocsPerRun(200, run); a != 0 {
		t.Errorf("%v allocations", a)
	}
}
