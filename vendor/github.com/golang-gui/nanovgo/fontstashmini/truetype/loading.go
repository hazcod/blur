package truetype

import (
	"errors"
)

// FontInfo is defined publically so you can declare on on the stack or as a
// global or etc, but you should treat it as opaque.
type FontInfo struct {
	data             []byte // contains the .ttf file
	fontStart        int    // offset of start of font
	loca             int    // table location as offset from start of .ttf
	head             int
	glyf             int
	hhea             int
	hmtx             int
	kern             int
	numGlyphs        int // number of glyphs, needed for range checking
	indexMap         int // a cmap mapping for our chosen character encoding
	indexToLocFormat int // format needed to map from glyph index to glyph
}

// GetFontCount returns the number of fonts in the data.
// For regular .ttf files, it returns 1.
// For .ttc (TrueType Collection) files, it returns the number of fonts in the collection.
// Returns 0 if the data is not a valid font file.
func GetFontCount(data []byte) int {
	if isFont(data) {
		return 1
	}
	if string(data[0:4]) == "ttcf" {
		if u32(data, 4) == 0x00010000 || u32(data, 4) == 0x00020000 {
			return int(u32(data, 8))
		}
	}
	return 0
}

// Each .ttf/.ttc file may have more than one font. Each font has a sequential
// index number starting from 0. Call this function to get the font offset for
// a given index; it returns -1 if the index is out of range. A regular .ttf
// file will only define one font and it always be at offset 0, so it will return
// '0' for index 0, and -1 for all other indices. You can just skip this step
// if you know it's that kind of font.
func GetFontOffsetForIndex(data []byte, index int) int {
	if isFont(data) {
		if index == 0 {
			return 0
		} else {
			return -1
		}
	}

	// Check if it's a TTC
	if string(data[0:4]) == "ttcf" {
		if u32(data, 4) == 0x00010000 || u32(data, 4) == 0x00020000 {
			n := int(u32(data, 8))
			if index >= n {
				return -1
			}
			return int(u32(data, 12+index*4))
		}
	}
	return -1
}

// GetFontName returns the fullname of the font at the given index.
// For .ttf files, use index 0.
// For .ttc files, use the appropriate font index (0-based).
// Returns an empty string if the font name cannot be retrieved.
func GetFontName(data []byte, index int) string {
	fontOffset := GetFontOffsetForIndex(data, index)
	if fontOffset < 0 {
		return ""
	}

	nameTable := findTable(data, fontOffset, "name")
	if nameTable == 0 {
		return ""
	}

	format := int(u16(data, nameTable))
	if format != 0 && format != 1 {
		return ""
	}

	count := int(u16(data, nameTable+2))
	stringOffset := int(u32(data, nameTable+4))

	recordsEnd := nameTable + 6 + count*12

	var stringsStart int
	if stringOffset == 0 || stringOffset >= len(data) {
		stringsStart = recordsEnd
	} else {
		calculatedPos := nameTable + stringOffset
		if calculatedPos >= len(data) || calculatedPos < nameTable {
			stringsStart = recordsEnd
		} else {
			stringsStart = calculatedPos
		}
	}

	for i := 0; i < count; i++ {
		recordOffset := nameTable + 6 + i*12
		if recordOffset+12 > len(data) {
			break
		}

		platformID := int(u16(data, recordOffset))
		encodingID := int(u16(data, recordOffset+2))
		languageID := int(u16(data, recordOffset+4))
		nameID := int(u16(data, recordOffset+6))
		length := int(u16(data, recordOffset+8))
		strOffsetInStrings := int(u16(data, recordOffset+10))

		if nameID == 4 && languageID == 1033 {
			strOffset := stringsStart + strOffsetInStrings
			if strOffset+length > len(data) || strOffset < nameTable {
				strOffsetAlt := recordsEnd + strOffsetInStrings
				if strOffsetAlt+length > len(data) || strOffsetAlt < nameTable {
					continue
				}
				strOffset = strOffsetAlt
			}

			if platformID == PLATFORM_ID_MICROSOFT && (encodingID == 1 || encodingID == 10) && length >= 2 && data[strOffset] == 0 {
				return decodeUTF16BE(data[strOffset : strOffset+length])
			}

			if platformID == PLATFORM_ID_UNICODE && (encodingID == 0 || encodingID == 1 || encodingID == 2) && length >= 2 && data[strOffset] == 0 {
				return decodeUTF16BE(data[strOffset : strOffset+length])
			}

			return string(data[strOffset : strOffset+length])
		}
	}
	return ""
}

func decodeUTF16BE(data []byte) string {
	result := make([]rune, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		c := uint16(data[i])<<8 | uint16(data[i+1])
		result = append(result, rune(c))
	}
	return string(result)
}

func decodeUTF16LE(data []byte) string {
	result := make([]rune, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		c := uint16(data[i+1])<<8 | uint16(data[i])
		result = append(result, rune(c))
	}
	return string(result)
}

// Given an offset into the file that defines a font, this function builds the
// necessary cached info for the rest of the system.
func InitFont(data []byte, offset int) (font *FontInfo, err error) {
	if len(data)-offset < 12 {
		err = errors.New("TTF data is too short")
		return
	}
	font = new(FontInfo)
	font.data = data
	font.fontStart = offset

	cmap := findTable(data, offset, "cmap")
	font.loca = findTable(data, offset, "loca")
	font.head = findTable(data, offset, "head")
	font.glyf = findTable(data, offset, "glyf")
	font.hhea = findTable(data, offset, "hhea")
	font.hmtx = findTable(data, offset, "hmtx")
	font.kern = findTable(data, offset, "kern")
	if cmap == 0 || font.loca == 0 || font.head == 0 || font.glyf == 0 || font.hhea == 0 || font.hmtx == 0 {
		err = errors.New("Required table not found")
		return
	}

	t := findTable(data, offset, "maxp")
	if t != 0 {
		font.numGlyphs = int(u16(data, t+4))
	} else {
		font.numGlyphs = 0xfff
	}

	numTables := int(u16(data, cmap+2))
	for i := 0; i < numTables; i++ {
		encodingRecord := cmap + 4 + 8*i
		switch int(u16(data, encodingRecord)) {
		case PLATFORM_ID_MICROSOFT:
			switch int(u16(data, encodingRecord+2)) {
			case MS_EID_UNICODE_FULL, MS_EID_UNICODE_BMP:
				font.indexMap = cmap + int(u32(data, encodingRecord+4))
			}
		}
	}

	if font.indexMap == 0 {
		err = errors.New("Unknown cmap encoding table")
		return
	}

	font.indexToLocFormat = int(u16(data, font.head+50))
	return
}
