package contextpacket

type EvidenceStoreFactory func(ClickHouseRows) (*ClickHouseEvidenceStore, error)

func NewEvidenceStoreFactory(codec *EvidenceIDCodec) EvidenceStoreFactory {
	return func(rows ClickHouseRows) (*ClickHouseEvidenceStore, error) {
		return NewClickHouseEvidenceStoreWithOptions(rows, EvidenceStoreOptions{Codec: codec})
	}
}
