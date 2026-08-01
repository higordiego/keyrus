package application

// ValidateTraceparent validates the canonical lowercase W3C trace-parent
// representation and rejects the reserved version and all-zero identifiers.
func ValidateTraceparent(value string) error {
	if len(value) != 55 || value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return ErrInvalidArgument
	}
	version := value[0:2]
	traceID := value[3:35]
	parentID := value[36:52]
	flags := value[53:55]
	if !lowerHex(version) || !lowerHex(traceID) || !lowerHex(parentID) || !lowerHex(flags) ||
		version == "ff" || allZeroHex(traceID) || allZeroHex(parentID) ||
		(version == "00" && flags != "00" && flags != "01") {
		return ErrInvalidArgument
	}
	return nil
}

func lowerHex(value string) bool {
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func allZeroHex(value string) bool {
	for index := range len(value) {
		if value[index] != '0' {
			return false
		}
	}
	return true
}
