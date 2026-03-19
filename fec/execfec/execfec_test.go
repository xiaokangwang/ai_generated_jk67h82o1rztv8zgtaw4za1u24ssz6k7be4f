package execfec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand"
	"os"
	"testing"
)

func TestEng222(t *testing.T) {
	feceng := NewExecFecEngineV2("/home/shelikhoo/proj/src/github.com/xiaokangwang/wirehairutil/target/release/wirehairutil")
	fi, err := os.Open("/home/shelikhoo/Downloads/cedict_1_0_ts_utf-8_mdbg.zip")
	if err != nil {
		panic(err)
	}
	s, _ := fi.Stat()
	fs := s.Size()
	encoder := feceng.GetEncoder(fi, 512*1024)
	vals := make(map[uint32][]byte)
	for i := 0; i <= 600; i++ {
		blockid := uint32(mrand.Int31())
		srd := encoder.GetShard(blockid)
		srdc := make([]byte, len(srd))
		copy(srdc, srd)
		vals[blockid] = srdc
		w := sha256.Sum256(srdc)
		fmt.Println(blockid, hex.EncodeToString(w[:]))
	}
	dec := feceng.GetDecoder(fs, 512*1024)
	/*
		for _ , _v := range []uint32{429, 525, 581, 299, 350, 421 , 100} {
			k := _v
			v := vals[k]*/
	for k, v := range vals {

		w := sha256.Sum256(v)
		fmt.Println(k, hex.EncodeToString(w[:]))

		ok := dec.PutShard(k, v)
		if ok {
			break
		}

	}
	hash := sha256.New()
	io.Copy(hash, dec.GetIn())
	fmt.Println(hex.EncodeToString(hash.Sum(nil)))

	encoder.Close()
	dec.Close()
}
