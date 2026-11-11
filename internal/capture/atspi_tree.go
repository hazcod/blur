package capture

import (
	"errors"
	"unicode/utf8"
)

func boundedATSPIChildCount(count int32, max int) (int, bool, error) {
	if count < 0 || max < 1 {
		return 0, false, errors.New("capture: invalid AT-SPI child count")
	}
	if uint64(count) > uint64(max) {
		return max, true, nil
	}
	return int(count), false, nil
}

func boundedATSPIText(text string, max int, more bool) (string, bool, error) {
	if max < 0 || !utf8.ValidString(text) {
		return "", false, errors.New("capture: invalid AT-SPI text")
	}
	bounded, truncated := truncateUTF8(text, max)
	return bounded, truncated || more, nil
}
