package wirehair

func testMessage(n int) []byte {
	msg := make([]byte, n)
	for i := range msg {
		msg[i] = byte((i*131 + 17 + i/7) ^ (i >> 3))
	}
	return msg
}

func selectShardIDs(n uint32) []uint32 {
	var ids []uint32
	for i := uint32(0); i < n; i++ {
		if i%4 != 1 {
			ids = append(ids, i)
		}
	}
	for next := n; len(ids) < int(n)+3; next++ {
		ids = append(ids, next)
	}
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids
}
