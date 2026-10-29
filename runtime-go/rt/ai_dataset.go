//go:build !js

package rt

// ai_dataset.go — the Std.Ai.Dataset kernels.
//
//	Ai_datasetReadLines : String -> Int -> Int -> Task Error RawLines
//	Ai_datasetCanonical : String -> Result Error String
//	Ai_datasetSha256File : String -> Task Error String
//	Ai_datasetCheckPattern : String -> Result Error ()
//
// Std.Ai.Dataset is pure Sky over these three. A dataset is read in chunks of
// lines (ReadLines is stateless: it opens the file, seeks to a byte offset,
// reads at most n lines and closes the file), so a large JSONL file never sits
// in memory. A frozen version is canonical JSONL: every row re-encoded with
// sorted object keys and no insignificant white space (Canonical), and its
// digest is the SHA-256 of the file's bytes (Sha256File, streamed).
//
// A file that cannot be read or a line that is not JSON is the typed
// `DatasetInvalid` failure, which names the file and the line, never the line's
// content: a dataset row can hold personal data.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// aiDatasetMaxLine bounds one line of a dataset file. A longer line is a
// DatasetInvalid failure rather than an unbounded allocation.
const aiDatasetMaxLine = 16 << 20

// aiDatasetFail builds the typed failure. reasons never carries row text.
func aiDatasetFail(dataset, reasons string) *aiFailure {
	return aiFailf(aiDatasetInvalid, "%s: %s", dataset, reasons).
		with("dataset", dataset).with("reasons", reasons)
}

// aiReadLines reads at most max lines from path starting at byte offset.
// It returns the lines without their line endings ("\r\n" or "\n"), the byte
// offset just past the last line returned, and whether the end of the file
// was reached.
func aiReadLines(path string, offset int64, max int) ([]string, int64, bool, error) {
	if max < 1 {
		max = 1
	}
	if offset < 0 {
		return nil, 0, false, aiDatasetFail(path, "negative offset")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, aiDatasetFail(path, "cannot open the file: "+aiPathErr(err))
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, false, aiDatasetFail(path, "cannot seek: "+aiPathErr(err))
	}
	r := bufio.NewReaderSize(f, 64<<10)
	lines := make([]string, 0, max)
	pos := offset
	for len(lines) < max {
		line, err := aiReadLine(r)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, false, aiDatasetFail(path, "line "+strconv.Itoa(len(lines)+1)+" after byte "+strconv.FormatInt(pos, 10)+": "+err.Error())
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return lines, pos, true, nil
		}
		pos += int64(len(line))
		lines = append(lines, strings.TrimRight(string(line), "\r\n"))
		if errors.Is(err, io.EOF) {
			return lines, pos, true, nil
		}
	}
	// Peek: a file that ends exactly here is done now, so the caller does not
	// make one more empty read.
	if _, err := r.Peek(1); errors.Is(err, io.EOF) {
		return lines, pos, true, nil
	}
	return lines, pos, false, nil
}

// aiReadLine reads one line including its "\n", refusing a line longer than
// aiDatasetMaxLine.
func aiReadLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > aiDatasetMaxLine {
			return nil, errors.New("the line is longer than 16 MiB")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

// aiPathErr is an OS error without the Go operation prefix repeated.
func aiPathErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// aiCanonicalJSON re-encodes one JSON value canonically: object keys sorted by
// their UTF-8 bytes, no white space between tokens, numbers kept as written,
// strings escaped by Go's encoder with HTML escaping off. Two encodings of the
// same value give the same bytes, so the digest of a frozen dataset does not
// depend on key order or spacing in the source.
func aiCanonicalJSON(text string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", aiJSONErr(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("more than one JSON value on the line")
	}
	var out bytes.Buffer
	if err := aiWriteCanonical(&out, v); err != nil {
		return "", err
	}
	return out.String(), nil
}

// aiJSONErr names a JSON syntax error by its byte offset only, never by the
// text around it.
func aiJSONErr(err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return errors.New("not JSON (byte " + strconv.FormatInt(se.Offset, 10) + ")")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("not JSON (the value is incomplete)")
	}
	return errors.New("not JSON")
}

func aiWriteCanonical(out *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := aiWriteString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := aiWriteCanonical(out, x[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := aiWriteCanonical(out, e); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		return aiWriteString(out, x)
	case json.Number:
		out.WriteString(x.String())
	case bool:
		if x {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return errors.New("an unsupported JSON value")
	}
	return nil
}

func aiWriteString(out *bytes.Buffer, s string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	out.Write(bytes.TrimRight(b.Bytes(), "\n"))
	return nil
}

// aiSha256File is the lowercase hex SHA-256 of a file's bytes, streamed.
func aiSha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", aiDatasetFail(path, "cannot open the file: "+aiPathErr(err))
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", aiDatasetFail(path, "cannot read the file: "+aiPathErr(err))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Ai_datasetReadLines : String -> Int -> Int -> Task Error RawLines
// (path, byte offset, maximum lines) -> { lines, next, done }.
func Ai_datasetReadLines(pathArg, offsetArg, maxArg any) any {
	path, offset, max := AsString(pathArg), int64(AsInt(offsetArg)), AsInt(maxArg)
	return func() any {
		lines, next, done, err := aiReadLines(path, offset, max)
		if err != nil {
			return aiTaskErr(err)
		}
		out := make([]any, len(lines))
		for i, l := range lines {
			out[i] = l
		}
		return Ok[any, any](map[string]any{"lines": out, "next": int(next), "done": done})
	}
}

// Ai_datasetCanonical : String -> Result Error String
func Ai_datasetCanonical(textArg any) any {
	out, err := aiCanonicalJSON(AsString(textArg))
	if err != nil {
		return Err[any, any](aiErrorValue(aiDatasetFail("row", err.Error())))
	}
	return Ok[any, any](out)
}

// Ai_datasetSha256File : String -> Task Error String
func Ai_datasetSha256File(pathArg any) any {
	path := AsString(pathArg)
	return func() any {
		d, err := aiSha256File(path)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](d)
	}
}

// Ai_datasetCheckPattern : String -> Result Error () — whether a user's
// Scrub.pattern compiles, as an error that names the scrubber (a rule that
// silently scrubbed nothing would leak the values it was written for).
func Ai_datasetCheckPattern(patternArg any) any {
	if _, err := regexp.Compile(AsString(patternArg)); err != nil {
		return Err[any, any](ErrInvalidInput("Std.Ai.Dataset.Scrub.pattern: the pattern does not compile: " + err.Error()))
	}
	return Ok[any, any](struct{}{})
}
