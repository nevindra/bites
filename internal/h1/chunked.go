package h1

// maxChunkDigits bounds the hex digits of a chunk size, leading zeros
// included, so a hostile "0000...0001" cannot run on for ever for free.
const maxChunkDigits = 16

var (
	errChunkSize     = refuse(400, true, "malformed chunk size")
	errChunkSyntax   = refuse(400, true, "malformed chunked framing")
	errChunkOverhead = refuse(431, true, "chunk extensions and trailers too large")
)

type chunkState uint8

const (
	csSize         chunkState = iota // hex digits of the size
	csSizeAfter                      // after the digits: BWS, ';' or CR
	csExt                            // chunk extension, up to CR
	csSizeLF                         // CR of the size line seen
	csData                           // chunk data
	csDataCR                         // expecting CR after data
	csDataLF                         // expecting LF after data
	csTrailerStart                   // start of a trailer line, or the blank line
	csTrailerLine                    // inside a trailer line
	csTrailerLF                      // CR of a trailer line seen
	csEndLF                          // CR of the final blank line seen
	csDone
)

// ChunkedDecoder decodes a chunked body incrementally. The zero value is not
// ready: call Reset first. It holds no buffers.
type ChunkedDecoder struct {
	state       chunkState
	size        uint64 // size being read
	remain      uint64 // data bytes left in the current chunk
	digits      int
	overhead    int
	maxOverhead int
}

// Reset readies the decoder for a new body. maxOverhead bounds the bytes of
// chunk extensions and trailers over the whole body (ADR 0045).
func (d *ChunkedDecoder) Reset(maxOverhead int) {
	*d = ChunkedDecoder{maxOverhead: maxOverhead}
}

// Decode consumes bytes from in and writes decoded body bytes to the start of
// out (it does not append). It returns how much of in it used and how much of
// out it filled. It stops early when out is full, so call it again with a
// fresh out and in[consumed:]. done is true once the final chunk and trailers
// have been read; the bytes after consumed belong to the next request.
func (d *ChunkedDecoder) Decode(in, out []byte) (consumed int, outN int, done bool, err error) {
	for consumed < len(in) {
		if d.state == csData {
			if outN == len(out) {
				return consumed, outN, false, nil
			}
			n := min(uint64(len(in)-consumed), uint64(len(out)-outN), d.remain)
			copy(out[outN:], in[consumed:consumed+int(n)])
			consumed += int(n)
			outN += int(n)
			d.remain -= n
			if d.remain == 0 {
				d.state = csDataCR
			}
			continue
		}
		b := in[consumed]
		consumed++
		switch d.state {
		case csSize:
			v, ok := hexVal(b)
			if !ok {
				if d.digits == 0 {
					return consumed, outN, false, errChunkSize
				}
				d.state = csSizeAfter
				consumed--
				continue
			}
			d.digits++
			if d.digits > maxChunkDigits || d.size > (1<<63-1)>>4 {
				return consumed, outN, false, errChunkSize
			}
			d.size = d.size<<4 | uint64(v)
		case csSizeAfter:
			switch b {
			case ' ', '\t':
				if err = d.spend(1); err != nil {
					return consumed, outN, false, err
				}
			case ';':
				if err = d.spend(1); err != nil {
					return consumed, outN, false, err
				}
				d.state = csExt
			case '\r':
				d.state = csSizeLF
			default:
				return consumed, outN, false, errChunkSyntax
			}
		case csExt:
			if b == '\r' {
				d.state = csSizeLF
				continue
			}
			if badLineByte(b) {
				return consumed, outN, false, errChunkSyntax
			}
			if err = d.spend(1); err != nil {
				return consumed, outN, false, err
			}
		case csSizeLF:
			if b != '\n' {
				return consumed, outN, false, errChunkSyntax
			}
			if d.size == 0 {
				d.state = csTrailerStart
			} else {
				d.remain, d.state = d.size, csData
			}
		case csDataCR:
			if b != '\r' {
				return consumed, outN, false, errChunkSyntax
			}
			d.state = csDataLF
		case csDataLF:
			if b != '\n' {
				return consumed, outN, false, errChunkSyntax
			}
			d.size, d.digits, d.state = 0, 0, csSize
		case csTrailerStart:
			switch {
			case b == '\r':
				d.state = csEndLF
			case b == ' ' || b == '\t': // obs-fold, as in a head
				return consumed, outN, false, errChunkSyntax
			case badLineByte(b):
				return consumed, outN, false, errChunkSyntax
			default:
				if err = d.spend(1); err != nil {
					return consumed, outN, false, err
				}
				d.state = csTrailerLine
			}
		case csTrailerLine:
			if b == '\r' {
				d.state = csTrailerLF
				continue
			}
			if badLineByte(b) {
				return consumed, outN, false, errChunkSyntax
			}
			if err = d.spend(1); err != nil {
				return consumed, outN, false, err
			}
		case csTrailerLF:
			if b != '\n' {
				return consumed, outN, false, errChunkSyntax
			}
			d.state = csTrailerStart
		case csEndLF:
			if b != '\n' {
				return consumed, outN, false, errChunkSyntax
			}
			d.state = csDone
			return consumed, outN, true, nil
		case csDone:
			return consumed - 1, outN, true, nil
		}
	}
	return consumed, outN, d.state == csDone, nil
}

func (d *ChunkedDecoder) spend(n int) error {
	d.overhead += n
	if d.overhead > d.maxOverhead {
		return errChunkOverhead
	}
	return nil
}

// badLineByte is a byte that may not appear inside a chunk extension or
// trailer line: NUL, a bare CR or LF, any other control except HTAB.
func badLineByte(b byte) bool {
	return (b < 0x20 && b != '\t') || b == 0x7f
}

func hexVal(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}
