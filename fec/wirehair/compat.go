package wirehair

type WirehairResult int

const (
	WirehairResultSuccess WirehairResult = iota
	WirehairResultNeedMore
	WirehairResultInternal
)

func parseCompatResult(code ResultCode) (WirehairResult, error) {
	switch code {
	case ResultSuccess:
		return WirehairResultSuccess, nil
	case ResultNeedMore:
		return WirehairResultNeedMore, nil
	default:
		return WirehairResultInternal, &Error{Code: code}
	}
}

func WirehairInit() error {
	return Init()
}

type WirehairEncoder struct {
	inner *Encoder
}

func NewWirehairEncoder(message []byte, messageSizeBytes uint64, blockSizeBytes uint32) (*WirehairEncoder, error) {
	if uint64(len(message)) < messageSizeBytes {
		return nil, &Error{Code: ResultInvalidInput}
	}
	inner, err := NewEncoder(message[:messageSizeBytes], blockSizeBytes)
	if err != nil {
		return nil, err
	}
	return &WirehairEncoder{inner: inner}, nil
}

func (e *WirehairEncoder) Encode(blockID uint64, block []byte, blockSize uint32, blockOutBytes *uint32) (WirehairResult, error) {
	if uint32(len(block)) < blockSize {
		return WirehairResultInternal, &Error{Code: ResultInvalidInput}
	}
	n, err := e.inner.Encode(uint32(blockID), block[:blockSize])
	if blockOutBytes != nil {
		*blockOutBytes = uint32(n)
	}
	if err != nil {
		if whErr, ok := err.(*Error); ok {
			return parseCompatResult(whErr.Code)
		}
		return WirehairResultInternal, err
	}
	return WirehairResultSuccess, nil
}

type WirehairDecoder struct {
	inner *Decoder
}

func NewWirehairDecoder(messageSizeBytes uint64, blockSizeBytes uint32) (*WirehairDecoder, error) {
	inner, err := NewDecoder(messageSizeBytes, blockSizeBytes)
	if err != nil {
		return nil, err
	}
	return &WirehairDecoder{inner: inner}, nil
}

func (d *WirehairDecoder) Decode(blockID uint64, block []byte, blockOutSizeBytes uint32) (WirehairResult, error) {
	if uint32(len(block)) < blockOutSizeBytes {
		return WirehairResultInternal, &Error{Code: ResultInvalidInput}
	}
	state, err := d.inner.Decode(uint32(blockID), block[:blockOutSizeBytes])
	if err != nil {
		if whErr, ok := err.(*Error); ok {
			return parseCompatResult(whErr.Code)
		}
		return WirehairResultInternal, err
	}
	if state == StateReady {
		return WirehairResultSuccess, nil
	}
	return WirehairResultNeedMore, nil
}

func (d *WirehairDecoder) Recover(message []byte, messageSizeBytes uint64) (WirehairResult, error) {
	if uint64(len(message)) < messageSizeBytes {
		return WirehairResultInternal, &Error{Code: ResultInvalidInput}
	}
	if err := d.inner.Recover(message[:messageSizeBytes]); err != nil {
		if whErr, ok := err.(*Error); ok {
			return parseCompatResult(whErr.Code)
		}
		return WirehairResultInternal, err
	}
	return WirehairResultSuccess, nil
}

func (d *WirehairDecoder) RecoverBlock(blockID uint64, block []byte, blockOutBytes *uint32) (WirehairResult, error) {
	n, err := d.inner.RecoverBlock(uint32(blockID), block)
	if blockOutBytes != nil {
		*blockOutBytes = uint32(n)
	}
	if err != nil {
		if whErr, ok := err.(*Error); ok {
			return parseCompatResult(whErr.Code)
		}
		return WirehairResultInternal, err
	}
	return WirehairResultSuccess, nil
}

func WirehairDecoderToEncoder(decoder *WirehairDecoder) (*WirehairEncoder, error) {
	if decoder == nil || decoder.inner == nil {
		return nil, &Error{Code: ResultInvalidInput}
	}
	inner, err := decoder.inner.BecomeEncoder()
	if err != nil {
		return nil, err
	}
	return &WirehairEncoder{inner: inner}, nil
}
