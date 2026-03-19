package transfer

const (
	ResponseTypeData  uint8 = 1
	ResponseTypeError uint8 = 2
)

const (
	PayloadTypeFile uint8 = 1
	PayloadTypeList uint8 = 2
)

type Request struct {
	Path       string
	FilePart   uint32
	TransferID uint64
}

type Client2Server struct {
	Seq        uint64
	TransferID uint64
	// Path To Send or List
	PathLen  uint16 `struc:"uint16,sizeof=Path"`
	Path     string
	FilePart uint32
	// ShardSize
	ShardSize uint32

	// Number of Packets to Send
	RecvWindow uint32
	// Packet Per Second
	RecvRate uint32
}

type Server2Client struct {
	ResponseType   uint8
	PayloadType    uint8
	Seq            uint64
	TransferID     uint64
	FileSize       uint64
	FileTotalParts uint32
	ShardSeq       uint32
	DataLen        uint16 `struc:"uint16,sizeof=Data"`
	Data           []byte
}
