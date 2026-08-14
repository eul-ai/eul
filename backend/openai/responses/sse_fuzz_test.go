package responses

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
)

const fuzzSSEMaximum = 16 * 1024

type fuzzSSECapture struct {
	text      string
	reasoning string
	tools     []agent.ToolCallSnapshot
}

type fuzzChunkReader struct {
	data []byte
	size int
}

func FuzzResponsesSSE(f *testing.F) {
	for _, seed := range []struct {
		stream string
		chunk  uint8
	}{
		{
			stream: "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			chunk:  1,
		},
		{
			stream: "data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"failed\",\"message\":\"try later\"}}\r\n\r\n",
			chunk:  7,
		},
		{
			stream: strings.Join([]string{
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call-1","name":"read","arguments":""}}`,
				`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"README.md\"}"}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call-1","name":"read","arguments":"{\"path\":\"README.md\"}"}}`,
				`data: {"type":"response.completed","response":{"status":"completed"}}`,
			}, "\n\n") + "\n\n",
			chunk: 13,
		},
		{
			stream: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thinking\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			chunk:  32,
		},
		{
			stream: "data: {\"type\":\ndata: \"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			chunk:  2,
		},
		{stream: "data: {\n\n", chunk: 3},
	} {
		f.Add([]byte(seed.stream), seed.chunk)
	}

	f.Fuzz(func(t *testing.T, stream []byte, chunkSeed uint8) {
		if len(stream) > fuzzSSEMaximum {
			t.Skip()
		}

		wholeResponse, wholeCapture, wholeErr := decodeFuzzSSE(bytes.NewReader(stream))
		chunkSize := int(chunkSeed)%32 + 1
		chunkedResponse, chunkedCapture, chunkedErr := decodeFuzzSSE(&fuzzChunkReader{data: stream, size: chunkSize})
		if !reflect.DeepEqual(chunkedResponse, wholeResponse) || !reflect.DeepEqual(chunkedCapture, wholeCapture) || errorString(chunkedErr) != errorString(wholeErr) {
			t.Fatalf("chunk size %d changed SSE result:\nwhole:   response=%#v capture=%#v error=%q\nchunked: response=%#v capture=%#v error=%q", chunkSize, wholeResponse, wholeCapture, errorString(wholeErr), chunkedResponse, chunkedCapture, errorString(chunkedErr))
		}

		if len(stream) == 0 {
			return
		}
		maximum := int64(len(stream) / 2)
		_, err := readSSE(bytes.NewReader(stream), maximum, func([]byte) (createResponseEnvelope, bool, error) {
			return createResponseEnvelope{}, false, nil
		})
		if err == nil {
			t.Fatalf("stream of %d bytes with limit %d returned %v", len(stream), maximum, err)
		}
	})
}

func decodeFuzzSSE(reader io.Reader) (createResponseEnvelope, fuzzSSECapture, error) {
	var capture fuzzSSECapture
	observer := &streamObserver{observer: agent.StreamObserver{
		Text: func(delta string) error {
			capture.text += delta
			return nil
		},
		Reasoning: func(delta string) error {
			capture.reasoning += delta
			return nil
		},
		ToolCall: func(snapshot agent.ToolCallSnapshot) error {
			capture.tools = append(capture.tools, snapshot)
			return nil
		},
	}}
	response, err := readResponsesSSE(reader, fuzzSSEMaximum, observer)
	return response, capture, err
}

func (reader *fuzzChunkReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	maximum := min(len(buffer), reader.size, len(reader.data))
	copied := copy(buffer, reader.data[:maximum])
	reader.data = reader.data[copied:]
	return copied, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
