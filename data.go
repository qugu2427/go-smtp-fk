package smtp

import (
	"bufio"
	"fmt"
	"io"
)

type EnhancedCode [3]int

// SMTPError specifies the error code, enhanced error code (if any) and
// message returned by the server.
type SMTPError struct {
	Code         int
	EnhancedCode EnhancedCode
	Message      string
}

// NoEnhancedCode is used to indicate that enhanced error code should not be
// included in response.
//
// Note that RFC 2034 requires an enhanced code to be included in all 2xx, 4xx
// and 5xx responses. This constant is exported for use by extensions, you
// should probably use EnhancedCodeNotSet instead.
var NoEnhancedCode = EnhancedCode{-1, -1, -1}

// EnhancedCodeNotSet is a nil value of EnhancedCode field in SMTPError, used
// to indicate that backend failed to provide enhanced status code. X.0.0 will
// be used (X is derived from error code).
var EnhancedCodeNotSet = EnhancedCode{0, 0, 0}

// CodePair represents a combination of
// a basic smtp code and an enhanced code.
type CodePair struct {
	Basic    int
	Enhanced EnhancedCode
}

// Populates a generic enhanced code for code
// pairs where enhanced code is not set.
func (c *CodePair) populateEnhancedCode() {
	if c.Enhanced == EnhancedCodeNotSet {
		cat := c.Basic / 100
		switch cat {
		case 2, 4, 5:
			c.Enhanced = EnhancedCode{cat, 0, 0}
		default:
			c.Enhanced = NoEnhancedCode
		}
	}
}

var (
	CodeReady             CodePair = CodePair{220, NoEnhancedCode}
	CodeBye               CodePair = CodePair{221, EnhancedCode{2, 0, 0}}
	CodeOk                CodePair = CodePair{250, EnhancedCode{2, 0, 0}}
	CodeCannotVerifyUser  CodePair = CodePair{252, EnhancedCode{2, 5, 0}}
	CodeStartMail         CodePair = CodePair{354, NoEnhancedCode}
	CodeNotAvailable      CodePair = CodePair{421, EnhancedCode{4, 0, 0}}
	CodeConnectionErr     CodePair = CodePair{421, EnhancedCode{4, 4, 0}}
	CodeBadConnection     CodePair = CodePair{421, EnhancedCode{4, 4, 2}}
	CodeTooBusy           CodePair = CodePair{421, EnhancedCode{4, 4, 5}}
	CodeActionAborted     CodePair = CodePair{451, EnhancedCode{4, 0, 0}}
	CodeTooManyRcpts      CodePair = CodePair{452, EnhancedCode{4, 5, 3}}
	CodeLineTooLong       CodePair = CodePair{500, EnhancedCode{5, 4, 0}}
	CodeMsgTooBig         CodePair = CodePair{552, EnhancedCode{5, 3, 4}}
	CodeTransactionFailed CodePair = CodePair{554, EnhancedCode{5, 0, 0}}

	CodeAuthSuccess   CodePair = CodePair{235, EnhancedCode{2, 7, 0}}
	CodeAuthChallenge CodePair = CodePair{334, NoEnhancedCode}
	CodeAuthFail      CodePair = CodePair{454, EnhancedCode{4, 7, 0}}

	CodeInvalidCmd      CodePair = CodePair{500, EnhancedCode{5, 5, 1}}
	CodeInvalidArg      CodePair = CodePair{501, EnhancedCode{5, 5, 4}}
	CodeInvalidSequence CodePair = CodePair{503, EnhancedCode{5, 5, 1}}

	CodeSyntaxErrCmd CodePair = CodePair{500, EnhancedCode{5, 5, 2}}
	CodeSyntaxErrArg CodePair = CodePair{501, EnhancedCode{5, 5, 2}}

	CodeNotImplementedCmd CodePair = CodePair{502, EnhancedCode{5, 5, 1}}
	CodeNotImplementedArg CodePair = CodePair{504, EnhancedCode{5, 5, 4}}

	CodeTlsRequired     CodePair = CodePair{523, EnhancedCode{5, 7, 10}}
	CodeTlsHandshakeErr CodePair = CodePair{550, EnhancedCode{5, 0, 0}}
)

func (err *SMTPError) Error() string {
	s := fmt.Sprintf("SMTP error %03d", err.Code)
	if err.Message != "" {
		s += ": " + err.Message
	}
	return s
}

func (err *SMTPError) Temporary() bool {
	return err.Code/100 == 4
}

var ErrDataTooLarge = &SMTPError{
	Code:         CodeMsgTooBig.Basic,
	EnhancedCode: CodeMsgTooBig.Enhanced,
	Message:      "Maximum message size exceeded",
}

type dataReader struct {
	r     *bufio.Reader
	state int

	limited bool
	n       int64 // Maximum bytes remaining
}

func newDataReader(c *Conn) *dataReader {
	dr := &dataReader{
		r: c.text.R,
	}

	if c.server.MaxMessageBytes > 0 {
		dr.limited = true
		dr.n = int64(c.server.MaxMessageBytes)
	}

	return dr
}

func (r *dataReader) Read(b []byte) (n int, err error) {
	if r.limited {
		if r.n <= 0 {
			return 0, ErrDataTooLarge
		}
		if int64(len(b)) > r.n {
			b = b[0:r.n]
		}
	}

	// Code below is taken from net/textproto with only one modification to
	// not rewrite CRLF -> LF.

	// Run data through a simple state machine to
	// elide leading dots and detect End-of-Data (<CR><LF>.<CR><LF>) line.
	const (
		stateBeginLine = iota // beginning of line; initial state; must be zero
		stateDot              // read . at beginning of line
		stateDotCR            // read .\r at beginning of line
		stateCR               // read \r (possibly at end of line)
		stateData             // reading data in middle of line
		stateEOF              // reached .\r\n end marker line
	)
	for n < len(b) && r.state != stateEOF {
		var c byte
		c, err = r.r.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			break
		}
		switch r.state {
		case stateBeginLine:
			if c == '.' {
				r.state = stateDot
				continue
			}
			if c == '\r' {
				r.state = stateCR
				break
			}
			r.state = stateData
		case stateDot:
			if c == '\r' {
				r.state = stateDotCR
				continue
			}
			r.state = stateData
		case stateDotCR:
			if c == '\n' {
				r.state = stateEOF
				continue
			}
			r.state = stateData
		case stateCR:
			if c == '\n' {
				r.state = stateBeginLine
				break
			}
			r.state = stateData
		case stateData:
			if c == '\r' {
				r.state = stateCR
			}
		}
		b[n] = c
		n++
	}
	if err == nil && r.state == stateEOF {
		err = io.EOF
	}

	if r.limited {
		r.n -= int64(n)
	}
	return
}
