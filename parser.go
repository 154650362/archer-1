package archer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dongzerun/archer/util"
)

var (
	_ Resp = (*SimpleResp)(nil)
	_ Resp = (*IntResp)(nil)
	_ Resp = (*ErrorResp)(nil)
	_ Resp = (*BulkResp)(nil)
	_ Resp = (*ArrayResp)(nil)

	SimpleType = "simple"
	ErrorType  = "error"
	IntType    = "int"
	BulkType   = "bulk"
	ArrayType  = "array"

	Space   = byte(' ')
	SimpSep = byte('+')
	ErrSep  = byte('-')
	IntSep  = byte(':')
	BulkSep = byte('$')
	ArrSep  = byte('*')
	CRLF    = []byte("\r\n")

	PING   = []byte("PING")
	PONG   = []byte("PONG")
	SELECT = []byte("SELECT")
	OK     = []byte("OK")
	QUIT   = []byte("QUIT")
	MOVED  = []byte("MOVED")
	ASK    = []byte("ASK")
	ASKING = []byte("ASKING")
)

// Response Interface based on: redis client protocol
// http://redis.io/topics/protocol
// there are five Resp type
// For Simple Strings the first byte of the reply is "+"
// For Errors the first byte of the reply is "-"
// For Integers the first byte of the reply is ":"
// For Bulk Strings the first byte of the reply is "$"
// For Arrays the first byte of the reply is "*"
type Resp interface {
	Encode() []byte
	String() string
	Type() string
	Length() int //只给ArrayResp使用，检测命令参数的个数，其它均为0
}

// 现在看，没必要分成 Cmd Args，直接全都是Args就好了
// 一定要判断好 裸命令ping quit和BulkResp中的$-1的情况
// \r\n的收尾也很重要，网络报文收发要完整
type BaseResp struct {
	Rtype string
	Args  [][]byte
}

func (br *BaseResp) String() string {
	return string(bytes.Join(br.Args, []byte{Space}))
}

func (br *BaseResp) Type() string {
	return br.Rtype
}

func (br *BaseResp) Length() int {
	return 0
}

type SimpleResp struct {
	BaseResp
}

func (sr *SimpleResp) Encode() []byte {
	if sr.Rtype != SimpleType {
		e := fmt.Sprintf("SimpleResp Encode Type error: %s, expected %s", sr.Rtype, SimpleType)
		panic(e)
	}

	var b bytes.Buffer
	b.WriteByte(SimpSep)
	b.Write(sr.Args[0])
	b.Write(CRLF)
	return b.Bytes()
}

type ErrorResp struct {
	BaseResp
}

func (er *ErrorResp) Encode() []byte {
	if er.Rtype != ErrorType {
		e := fmt.Sprintf("ErrorResp Encode Type error: %s, expected %s", er.Rtype, ErrorType)
		panic(e)
	}

	var b bytes.Buffer
	b.WriteByte(ErrSep)
	b.Write(er.Args[0])
	b.Write(CRLF)
	return b.Bytes()
}

type IntResp struct {
	BaseResp
}

func (ir *IntResp) Encode() []byte {
	if ir.Rtype != IntType {
		e := fmt.Sprintf("IntResp Encode Type error: %s, expected %s", ir.Rtype, IntType)
		panic(e)
	}

	var b bytes.Buffer
	b.WriteByte(IntSep)
	b.Write(ir.Args[0])
	b.Write(CRLF)
	return b.Bytes()
}

type BulkResp struct {
	BaseResp
	Empty bool
}

func (br *BulkResp) Encode() []byte {
	if br.Rtype != BulkType {
		e := fmt.Sprintf("BulkResp Encode Type error: %s, expected %s", br.Rtype, BulkType)
		panic(e)
	}

	if br.Empty {
		return []byte("$-1\r\n")
	}

	var b bytes.Buffer
	b.WriteByte(BulkSep)
	b.Write(util.Itob(len(br.Args[0])))
	b.Write(CRLF)
	b.Write(br.Args[0])
	b.Write(CRLF)
	return b.Bytes()
}

type ArrayResp struct {
	BaseResp
	Args []*BulkResp
}

func (ar *ArrayResp) String() string {
	var str []string
	for _, i := range ar.Args {
		str = append(str, i.String())
	}
	return strings.Join(str, " ")
}

func (ar *ArrayResp) Encode() []byte {
	if ar.Rtype != ArrayType {
		e := fmt.Sprintf("ArrayResp Encode Type error: %s, expected %s", ar.Rtype, ArrayType)
		panic(e)
	}

	var b bytes.Buffer
	b.WriteByte(ArrSep)
	b.Write(util.Itob(len(ar.Args)))
	b.Write(CRLF)

	for _, arg := range ar.Args {
		b.Write(arg.Encode())
	}
	return b.Bytes()
}

func (ar *ArrayResp) Length() int {
	return len(ar.Args) - 1
}

func WriteRawByte(w *bufio.Writer, data []byte) error {
	_, err := w.Write(data)
	if err != nil {
		return err
	}
	err = w.Flush()
	if err != nil {
		return err
	}
	return nil
}

func WriteProtocol(w *bufio.Writer, r Resp) error {
	return WriteRawByte(w, r.Encode())
}

// binary data  may contain \r\n
// so ,we must read fixed-length data by io.ReadFull
func ReadProtocol(r *bufio.Reader) (Resp, error) {
	res, err := r.ReadBytes(byte('\n'))
	if err != nil {
		return nil, err
	}

	switch res[0] {
	case SimpSep:
		sr := &SimpleResp{}
		sr.Rtype = SimpleType
		sr.Args = append(sr.Args, res[1:len(res)-2])
		return sr, nil
	case ErrSep:
		er := &ErrorResp{}
		er.Rtype = ErrorType
		er.Args = append(er.Args, res[1:len(res)-2])
		return er, nil
	case IntSep:
		ir := &IntResp{}
		ir.Rtype = IntType
		ir.Args = append(ir.Args, res[1:len(res)-2])
		return ir, nil
	case BulkSep:
		br := &BulkResp{}
		br.Rtype = BulkType
		l, err := util.ParseLen(res[1 : len(res)-2])
		if err != nil {
			return nil, err
		}
		if l == -1 {
			br.Empty = true
			return br, nil
		}

		// 把\r\n也读出来，扔掉
		buf := make([]byte, l+2)
		n, e := io.ReadFull(r, buf)
		if e != nil || n != l+2 {
			return nil, err
		}
		br.Args = append(br.Args, buf[:len(buf)-2])
		return br, nil
	case ArrSep:
		ar := &ArrayResp{}
		ar.Rtype = ArrayType
		n, err := util.ParseLen(res[1 : len(res)-2])
		if err != nil {
			return nil, err
		}

		// must followed by n BulkResp
		for i := 0; i < n; i++ {
			rsp, err := ReadProtocol(r)
			if err != nil {
				return nil, err
			}
			br, ok := rsp.(*BulkResp)
			if !ok {
				return nil, errors.New("In  ReadResp ArrSep, must read BulkResp")
			}
			ar.Args = append(ar.Args, br)
		}
		return ar, nil
	case byte('Q'):
		fallthrough
	case byte('q'):
		if len(res) != 6 {
			return nil, errors.New("raw command must be quit or ping")
		}
		ar := &ArrayResp{}
		ar.Rtype = ArrayType
		br := &BulkResp{}
		br.Rtype = BulkType
		br.Args = [][]byte{[]byte("QUIT")}
		ar.Args = append(ar.Args, br)
		return ar, nil
	case byte('p'):
		fallthrough
	case byte('P'):
		if len(res) != 6 {
			return nil, errors.New("raw command must be quit or ping")
		}
		ar := &ArrayResp{}
		ar.Rtype = ArrayType
		br := &BulkResp{}
		br.Rtype = BulkType
		br.Args = [][]byte{[]byte("PING")}
		ar.Args = append(ar.Args, br)
		return ar, nil
	}

	return nil, errors.New("ReadResp error, unexpected: " + string(res) + string(res[0]))
}