package wirehair

import "fmt"

const (
	Version = 2

	minN         = 2
	maxN         = 64000
	refListMax   = 32
	maxDenseRows = 500
	maxExtraRows = 32

	heavyRows = 6
	heavyCols = 18
)

type ResultCode int32

const (
	ResultSuccess ResultCode = iota
	ResultNeedMore
	ResultInvalidInput
	ResultBadDenseSeed
	ResultBadPeelSeed
	ResultBadInputSmallN
	ResultBadInputLargeN
	ResultExtraInsufficient
	ResultError
	ResultOOM
	ResultUnsupportedPlatform
)

func (r ResultCode) String() string {
	switch r {
	case ResultSuccess:
		return "Wirehair_Success"
	case ResultNeedMore:
		return "Wirehair_NeedMore"
	case ResultInvalidInput:
		return "Wirehair_InvalidInput"
	case ResultBadDenseSeed:
		return "Wirehair_BadDenseSeed"
	case ResultBadPeelSeed:
		return "Wirehair_BadPeelSeed"
	case ResultBadInputSmallN:
		return "Wirehair_BadInput_SmallN"
	case ResultBadInputLargeN:
		return "Wirehair_BadInput_LargeN"
	case ResultExtraInsufficient:
		return "Wirehair_ExtraInsufficient"
	case ResultOOM:
		return "Wirehair_OOM"
	case ResultUnsupportedPlatform:
		return "Wirehair_UnsupportedPlatform"
	default:
		return "Unknown"
	}
}

type Error struct {
	Code ResultCode
}

func (e *Error) Error() string {
	switch e.Code {
	case ResultInvalidInput:
		return "a function parameter was invalid"
	case ResultBadDenseSeed:
		return "encoder needs a better dense seed"
	case ResultBadPeelSeed:
		return "encoder needs a better peel seed"
	case ResultBadInputSmallN:
		return "N is too small; reduce block size or use a larger message"
	case ResultBadInputLargeN:
		return "N is too large; increase block size or use a smaller message"
	case ResultExtraInsufficient:
		return "not enough extra rows to solve the matrix"
	case ResultError:
		return "unexpected wirehair error"
	case ResultOOM:
		return "out of memory"
	case ResultUnsupportedPlatform:
		return "platform is not supported"
	default:
		return fmt.Sprintf("wirehair error %d", e.Code)
	}
}

func resultError(code ResultCode) error {
	if code == ResultSuccess || code == ResultNeedMore {
		return nil
	}
	return &Error{Code: code}
}

type State int

const (
	StateNeedMore State = iota
	StateReady
)

type mode int

const (
	modeUnknown mode = iota
	modeEncoder
	modeDecoder
)

type markType uint8

const (
	markTodo markType = iota
	markPeel
	markDefer
)

type peelRowResult struct {
	PeelColumn uint16
	IsCopied   uint8
}

type peelOverlappingFields struct {
	Unmarked [2]uint16
	Result   peelRowResult
}

type peelRow struct {
	RecoveryID    uint32
	NextRow       uint16
	Params        PeelRowParameters
	UnmarkedCount uint16
	Marks         peelOverlappingFields
}

type peelColumn struct {
	Next        uint16
	Weight2Refs uint16
	PeelRow     uint16
	GEColumn    uint16
	Mark        markType
}

type peelRefs struct {
	RowCount uint16
	Rows     [refListMax]uint16
}
