package sky_wrappers

import (
	"bufio"
	"io"
)

func Sky_bufio_NewReadWriter(arg0 any, arg1 any) *bufio.ReadWriter {
	_arg0 := arg0.(*bufio.Reader)
	_arg1 := arg1.(*bufio.Writer)
	return bufio.NewReadWriter(_arg0, _arg1)
}

func Sky_bufio_NewReader(arg0 any) *bufio.Reader {
	_arg0 := arg0.(io.Reader)
	return bufio.NewReader(_arg0)
}

func Sky_bufio_NewReaderSize(arg0 any, arg1 any) *bufio.Reader {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.(int)
	return bufio.NewReaderSize(_arg0, _arg1)
}

func Sky_bufio_NewScanner(arg0 any) *bufio.Scanner {
	_arg0 := arg0.(io.Reader)
	return bufio.NewScanner(_arg0)
}

func Sky_bufio_NewWriter(arg0 any) *bufio.Writer {
	_arg0 := arg0.(io.Writer)
	return bufio.NewWriter(_arg0)
}

func Sky_bufio_NewWriterSize(arg0 any, arg1 any) *bufio.Writer {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(int)
	return bufio.NewWriterSize(_arg0, _arg1)
}

func Sky_bufio_ScanBytes(arg0 any, arg1 any) (int, []byte, error) {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(bool)
	return bufio.ScanBytes(_arg0, _arg1)
}

func Sky_bufio_ScanLines(arg0 any, arg1 any) (int, []byte, error) {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(bool)
	return bufio.ScanLines(_arg0, _arg1)
}

func Sky_bufio_ScanRunes(arg0 any, arg1 any) (int, []byte, error) {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(bool)
	return bufio.ScanRunes(_arg0, _arg1)
}

func Sky_bufio_ScanWords(arg0 any, arg1 any) (int, []byte, error) {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(bool)
	return bufio.ScanWords(_arg0, _arg1)
}

func Sky_bufio_ErrAdvanceTooFar() any {
	return bufio.ErrAdvanceTooFar
}

func Sky_bufio_ErrBadReadCount() any {
	return bufio.ErrBadReadCount
}

func Sky_bufio_ErrBufferFull() any {
	return bufio.ErrBufferFull
}

func Sky_bufio_ErrFinalToken() any {
	return bufio.ErrFinalToken
}

func Sky_bufio_ErrInvalidUnreadByte() any {
	return bufio.ErrInvalidUnreadByte
}

func Sky_bufio_ErrInvalidUnreadRune() any {
	return bufio.ErrInvalidUnreadRune
}

func Sky_bufio_ErrNegativeAdvance() any {
	return bufio.ErrNegativeAdvance
}

func Sky_bufio_ErrNegativeCount() any {
	return bufio.ErrNegativeCount
}

func Sky_bufio_ErrTooLong() any {
	return bufio.ErrTooLong
}

func Sky_bufio_MaxScanTokenSize() any {
	return bufio.MaxScanTokenSize
}

func Sky_bufio_ReadWriterAvailable(this any) int {
	_this := this.(*bufio.ReadWriter)

	return _this.Available()
}

func Sky_bufio_ReadWriterAvailableBuffer(this any) []byte {
	_this := this.(*bufio.ReadWriter)

	return _this.AvailableBuffer()
}

func Sky_bufio_ReadWriterDiscard(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(int)
	res, err := _this.Discard(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterFlush(this any) SkyResult {
	_this := this.(*bufio.ReadWriter)

	err := _this.Flush()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReadWriterPeek(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(int)
	res, err := _this.Peek(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterRead(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReadByte(this any) SkyResult {
	_this := this.(*bufio.ReadWriter)

	res, err := _this.ReadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReadBytes(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(byte)
	res, err := _this.ReadBytes(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReadFrom(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(io.Reader)
	res, err := _this.ReadFrom(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReadLine(this any) ([]byte, bool, error) {
	_this := this.(*bufio.ReadWriter)

	return _this.ReadLine()
}

func Sky_bufio_ReadWriterReadRune(this any) (rune, int, error) {
	_this := this.(*bufio.ReadWriter)

	return _this.ReadRune()
}

func Sky_bufio_ReadWriterReadSlice(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(byte)
	res, err := _this.ReadSlice(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReadString(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(byte)
	res, err := _this.ReadString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterUnreadByte(this any) SkyResult {
	_this := this.(*bufio.ReadWriter)

	err := _this.UnreadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReadWriterUnreadRune(this any) SkyResult {
	_this := this.(*bufio.ReadWriter)

	err := _this.UnreadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReadWriterWrite(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterWriteByte(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(byte)
	err := _this.WriteByte(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReadWriterWriteRune(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(rune)
	res, err := _this.WriteRune(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterWriteString(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(string)
	res, err := _this.WriteString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterWriteTo(this any, arg0 any) SkyResult {
	_this := this.(*bufio.ReadWriter)
	_arg0 := arg0.(io.Writer)
	res, err := _this.WriteTo(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReadWriterReader(this any) *bufio.Reader {
	_this := this.(*bufio.ReadWriter)

	return _this.Reader
}

func Sky_bufio_ReadWriterWriter(this any) *bufio.Writer {
	_this := this.(*bufio.ReadWriter)

	return _this.Writer
}

func Sky_bufio_ReaderBuffered(this any) int {
	_this := this.(*bufio.Reader)

	return _this.Buffered()
}

func Sky_bufio_ReaderDiscard(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(int)
	res, err := _this.Discard(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderPeek(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(int)
	res, err := _this.Peek(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderRead(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderReadByte(this any) SkyResult {
	_this := this.(*bufio.Reader)

	res, err := _this.ReadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderReadBytes(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(byte)
	res, err := _this.ReadBytes(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderReadLine(this any) ([]byte, bool, error) {
	_this := this.(*bufio.Reader)

	return _this.ReadLine()
}

func Sky_bufio_ReaderReadRune(this any) (rune, int, error) {
	_this := this.(*bufio.Reader)

	return _this.ReadRune()
}

func Sky_bufio_ReaderReadSlice(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(byte)
	res, err := _this.ReadSlice(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderReadString(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(byte)
	res, err := _this.ReadString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ReaderReset(this any, arg0 any) any {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(io.Reader)
	_this.Reset(_arg0)
	return struct{}{}
}

func Sky_bufio_ReaderSize(this any) int {
	_this := this.(*bufio.Reader)

	return _this.Size()
}

func Sky_bufio_ReaderUnreadByte(this any) SkyResult {
	_this := this.(*bufio.Reader)

	err := _this.UnreadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReaderUnreadRune(this any) SkyResult {
	_this := this.(*bufio.Reader)

	err := _this.UnreadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ReaderWriteTo(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Reader)
	_arg0 := arg0.(io.Writer)
	res, err := _this.WriteTo(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_ScannerBuffer(this any, arg0 any, arg1 any) any {
	_this := this.(*bufio.Scanner)
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int)
	_this.Buffer(_arg0, _arg1)
	return struct{}{}
}

func Sky_bufio_ScannerBytes(this any) []byte {
	_this := this.(*bufio.Scanner)

	return _this.Bytes()
}

func Sky_bufio_ScannerErr(this any) SkyResult {
	_this := this.(*bufio.Scanner)

	err := _this.Err()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_ScannerScan(this any) bool {
	_this := this.(*bufio.Scanner)

	return _this.Scan()
}

func Sky_bufio_ScannerSplit(this any, arg0 any) any {
	_this := this.(*bufio.Scanner)
	_arg0 := arg0.(bufio.SplitFunc)
	_this.Split(_arg0)
	return struct{}{}
}

func Sky_bufio_ScannerText(this any) string {
	_this := this.(*bufio.Scanner)

	return _this.Text()
}

func Sky_bufio_WriterAvailable(this any) int {
	_this := this.(*bufio.Writer)

	return _this.Available()
}

func Sky_bufio_WriterAvailableBuffer(this any) []byte {
	_this := this.(*bufio.Writer)

	return _this.AvailableBuffer()
}

func Sky_bufio_WriterBuffered(this any) int {
	_this := this.(*bufio.Writer)

	return _this.Buffered()
}

func Sky_bufio_WriterFlush(this any) SkyResult {
	_this := this.(*bufio.Writer)

	err := _this.Flush()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_WriterReadFrom(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.(io.Reader)
	res, err := _this.ReadFrom(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_WriterReset(this any, arg0 any) any {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.(io.Writer)
	_this.Reset(_arg0)
	return struct{}{}
}

func Sky_bufio_WriterSize(this any) int {
	_this := this.(*bufio.Writer)

	return _this.Size()
}

func Sky_bufio_WriterWrite(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_WriterWriteByte(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.(byte)
	err := _this.WriteByte(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_bufio_WriterWriteRune(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.(rune)
	res, err := _this.WriteRune(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_bufio_WriterWriteString(this any, arg0 any) SkyResult {
	_this := this.(*bufio.Writer)
	_arg0 := arg0.(string)
	res, err := _this.WriteString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

