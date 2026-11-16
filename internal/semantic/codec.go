package semantic

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
)

const documentMagic = "BLRSEM01"
const maxDocumentHeaderBytes = 64 << 10

// EncodeDocument is a bounded plaintext codec for the authenticated durable
// store. Vectors use exact float32 bits rather than larger decimal JSON. The
// caller must encrypt the result before persistence and clear it afterward.
func EncodeDocument(document Document) ([]byte, error) {
	if document.Validate() != nil {
		return nil, ErrInvalid
	}
	header, err := json.Marshal(document)
	if err != nil || len(header) > maxDocumentHeaderBytes {
		clear(header)
		return nil, ErrInvalid
	}
	defer clear(header)
	size := 12 + len(header) + len(document.Chunks)*document.Model.Dimensions*4
	if size > MaxDocumentBytes {
		return nil, ErrInvalid
	}
	output := make([]byte, size)
	copy(output, documentMagic)
	binary.BigEndian.PutUint32(output[8:12], uint32(len(header)))
	copy(output[12:], header)
	position := 12 + len(header)
	for _, chunk := range document.Chunks {
		for _, value := range chunk.Vector {
			binary.LittleEndian.PutUint32(output[position:position+4], math.Float32bits(value))
			position += 4
		}
	}
	return output, nil
}

func DecodeDocument(encoded []byte) (Document, error) {
	if len(encoded) < 12 || len(encoded) > MaxDocumentBytes || string(encoded[:8]) != documentMagic {
		return Document{}, ErrInvalid
	}
	headerBytes := int(binary.BigEndian.Uint32(encoded[8:12]))
	if headerBytes < 1 || headerBytes > maxDocumentHeaderBytes || headerBytes > len(encoded)-12 {
		return Document{}, ErrInvalid
	}
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(encoded[12 : 12+headerBytes]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || document.Model.Validate() != nil || len(document.Chunks) > MaxChunksPerSource {
		return Document{}, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(encoded) != 12+headerBytes+len(document.Chunks)*document.Model.Dimensions*4 {
		return Document{}, ErrInvalid
	}
	position := 12 + headerBytes
	for index := range document.Chunks {
		vector := make([]float32, document.Model.Dimensions)
		for dimension := range vector {
			vector[dimension] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[position : position+4]))
			position += 4
		}
		document.Chunks[index].Vector = vector
	}
	if document.Validate() != nil {
		clearDocument(document)
		return Document{}, ErrInvalid
	}
	return document, nil
}
