package sky_wrappers

import (
	"io"
)

func Sky_io_Copy(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(io.Reader)
	res, err := io.Copy(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_CopyBuffer(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(io.Reader)
	_arg2 := arg2.([]byte)
	res, err := io.CopyBuffer(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_CopyN(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(io.Reader)
	_arg2 := arg2.(int64)
	res, err := io.CopyN(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_LimitReader(arg0 any, arg1 any) io.Reader {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.(int64)
	return io.LimitReader(_arg0, _arg1)
}

func Sky_io_MultiReader(arg0 any) io.Reader {
	var _arg0 []io.Reader
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(io.Reader))
	}
	return io.MultiReader(_arg0...)
}

func Sky_io_MultiWriter(arg0 any) io.Writer {
	var _arg0 []io.Writer
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(io.Writer))
	}
	return io.MultiWriter(_arg0...)
}

func Sky_io_NewOffsetWriter(arg0 any, arg1 any) *io.OffsetWriter {
	_arg0 := arg0.(io.WriterAt)
	_arg1 := arg1.(int64)
	return io.NewOffsetWriter(_arg0, _arg1)
}

func Sky_io_NewSectionReader(arg0 any, arg1 any, arg2 any) *io.SectionReader {
	_arg0 := arg0.(io.ReaderAt)
	_arg1 := arg1.(int64)
	_arg2 := arg2.(int64)
	return io.NewSectionReader(_arg0, _arg1, _arg2)
}

func Sky_io_NopCloser(arg0 any) io.ReadCloser {
	_arg0 := arg0.(io.Reader)
	return io.NopCloser(_arg0)
}

func Sky_io_Pipe() any {
	_r0, _r1 := io.Pipe()
	return Tuple2{V0: _r0, V1: _r1}
}

func Sky_io_ReadAll(arg0 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	res, err := io.ReadAll(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ReadAtLeast(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.([]byte)
	_arg2 := arg2.(int)
	res, err := io.ReadAtLeast(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ReadFull(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.([]byte)
	res, err := io.ReadFull(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_TeeReader(arg0 any, arg1 any) io.Reader {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.(io.Writer)
	return io.TeeReader(_arg0, _arg1)
}

func Sky_io_WriteString(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(string)
	res, err := io.WriteString(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_Discard() any {
	return io.Discard
}

func Sky_io_EOF() any {
	return io.EOF
}

func Sky_io_ErrClosedPipe() any {
	return io.ErrClosedPipe
}

func Sky_io_ErrNoProgress() any {
	return io.ErrNoProgress
}

func Sky_io_ErrShortBuffer() any {
	return io.ErrShortBuffer
}

func Sky_io_ErrShortWrite() any {
	return io.ErrShortWrite
}

func Sky_io_ErrUnexpectedEOF() any {
	return io.ErrUnexpectedEOF
}

func Sky_io_SeekCurrent() any {
	return io.SeekCurrent
}

func Sky_io_SeekEnd() any {
	return io.SeekEnd
}

func Sky_io_SeekStart() any {
	return io.SeekStart
}

func Sky_io_ByteReaderReadByte(this any) SkyResult {
	_this := this.(io.ByteReader)

	res, err := _this.ReadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ByteScannerUnreadByte(this any) SkyResult {
	_this := this.(io.ByteScanner)

	err := _this.UnreadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_ByteWriterWriteByte(this any, arg0 any) SkyResult {
	_this := this.(io.ByteWriter)
	_arg0 := arg0.(byte)
	err := _this.WriteByte(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_CloserClose(this any) SkyResult {
	_this := this.(io.Closer)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_LimitedReaderRead(this any, arg0 any) SkyResult {
	var _this *io.LimitedReader
	if _p, ok := this.(*io.LimitedReader); ok { _this = _p } else { _v := this.(io.LimitedReader); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_LimitedReaderR(this any) io.Reader {
	var _this *io.LimitedReader
	if _p, ok := this.(*io.LimitedReader); ok { _this = _p } else { _v := this.(io.LimitedReader); _this = &_v }

	return _this.R
}

func Sky_io_LimitedReaderN(this any) int64 {
	var _this *io.LimitedReader
	if _p, ok := this.(*io.LimitedReader); ok { _this = _p } else { _v := this.(io.LimitedReader); _this = &_v }

	return _this.N
}

func Sky_io_OffsetWriterSeek(this any, arg0 any, arg1 any) SkyResult {
	var _this *io.OffsetWriter
	if _p, ok := this.(*io.OffsetWriter); ok { _this = _p } else { _v := this.(io.OffsetWriter); _this = &_v }
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int)
	res, err := _this.Seek(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_OffsetWriterWrite(this any, arg0 any) SkyResult {
	var _this *io.OffsetWriter
	if _p, ok := this.(*io.OffsetWriter); ok { _this = _p } else { _v := this.(io.OffsetWriter); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_OffsetWriterWriteAt(this any, arg0 any, arg1 any) SkyResult {
	var _this *io.OffsetWriter
	if _p, ok := this.(*io.OffsetWriter); ok { _this = _p } else { _v := this.(io.OffsetWriter); _this = &_v }
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.WriteAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_PipeReaderClose(this any) SkyResult {
	var _this *io.PipeReader
	if _p, ok := this.(*io.PipeReader); ok { _this = _p } else { _v := this.(io.PipeReader); _this = &_v }

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_PipeReaderCloseWithError(this any, arg0 any) SkyResult {
	var _this *io.PipeReader
	if _p, ok := this.(*io.PipeReader); ok { _this = _p } else { _v := this.(io.PipeReader); _this = &_v }
	_arg0 := arg0.(error)
	err := _this.CloseWithError(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_PipeReaderRead(this any, arg0 any) SkyResult {
	var _this *io.PipeReader
	if _p, ok := this.(*io.PipeReader); ok { _this = _p } else { _v := this.(io.PipeReader); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_PipeWriterClose(this any) SkyResult {
	var _this *io.PipeWriter
	if _p, ok := this.(*io.PipeWriter); ok { _this = _p } else { _v := this.(io.PipeWriter); _this = &_v }

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_PipeWriterCloseWithError(this any, arg0 any) SkyResult {
	var _this *io.PipeWriter
	if _p, ok := this.(*io.PipeWriter); ok { _this = _p } else { _v := this.(io.PipeWriter); _this = &_v }
	_arg0 := arg0.(error)
	err := _this.CloseWithError(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_PipeWriterWrite(this any, arg0 any) SkyResult {
	var _this *io.PipeWriter
	if _p, ok := this.(*io.PipeWriter); ok { _this = _p } else { _v := this.(io.PipeWriter); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ReaderRead(this any, arg0 any) SkyResult {
	_this := this.(io.Reader)
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ReaderAtReadAt(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(io.ReaderAt)
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.ReadAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_ReaderFromReadFrom(this any, arg0 any) SkyResult {
	_this := this.(io.ReaderFrom)
	_arg0 := arg0.(io.Reader)
	res, err := _this.ReadFrom(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_RuneReaderReadRune(this any) SkyResult {
	_this := this.(io.RuneReader)

	_r0, _r1, err := _this.ReadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(Tuple2{V0: _r0, V1: _r1})
}

func Sky_io_RuneScannerUnreadRune(this any) SkyResult {
	_this := this.(io.RuneScanner)

	err := _this.UnreadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_io_SectionReaderOuter(this any) any {
	var _this *io.SectionReader
	if _p, ok := this.(*io.SectionReader); ok { _this = _p } else { _v := this.(io.SectionReader); _this = &_v }

	_r0, _r1, _r2 := _this.Outer()
	return Tuple3{V0: _r0, V1: _r1, V2: _r2}
}

func Sky_io_SectionReaderRead(this any, arg0 any) SkyResult {
	var _this *io.SectionReader
	if _p, ok := this.(*io.SectionReader); ok { _this = _p } else { _v := this.(io.SectionReader); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_SectionReaderReadAt(this any, arg0 any, arg1 any) SkyResult {
	var _this *io.SectionReader
	if _p, ok := this.(*io.SectionReader); ok { _this = _p } else { _v := this.(io.SectionReader); _this = &_v }
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.ReadAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_SectionReaderSeek(this any, arg0 any, arg1 any) SkyResult {
	var _this *io.SectionReader
	if _p, ok := this.(*io.SectionReader); ok { _this = _p } else { _v := this.(io.SectionReader); _this = &_v }
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int)
	res, err := _this.Seek(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_SectionReaderSize(this any) int64 {
	var _this *io.SectionReader
	if _p, ok := this.(*io.SectionReader); ok { _this = _p } else { _v := this.(io.SectionReader); _this = &_v }

	return _this.Size()
}

func Sky_io_SeekerSeek(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(io.Seeker)
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int)
	res, err := _this.Seek(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_StringWriterWriteString(this any, arg0 any) SkyResult {
	_this := this.(io.StringWriter)
	_arg0 := arg0.(string)
	res, err := _this.WriteString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_WriterWrite(this any, arg0 any) SkyResult {
	_this := this.(io.Writer)
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_WriterAtWriteAt(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(io.WriterAt)
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.WriteAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_io_WriterToWriteTo(this any, arg0 any) SkyResult {
	_this := this.(io.WriterTo)
	_arg0 := arg0.(io.Writer)
	res, err := _this.WriteTo(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

