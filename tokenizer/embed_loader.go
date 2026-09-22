package tokenizer

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkoukk/tiktoken-go"
)

// BPE mergeable-ranks files for the two encodings used at runtime:
//   - cl100k_base — GPT-4 / GPT-3.5 / text-embedding-3-*
//   - o200k_base  — GPT-4o / o1 / o3 / GPT-4.1 / GPT-4.5
//
// Embedding them eliminates the lazy HTTP download that tiktoken-go's
// default loader performs on first use (from openaipublic.blob.core.windows.net),
// which blocks for the full TCP timeout on network-restricted hosts and
// causes the memory.fetch stage to exceed the client request deadline.
//
//go:embed data/cl100k_base.tiktoken
var cl100kBaseData []byte

//go:embed data/o200k_base.tiktoken
var o200kBaseData []byte

const (
	urlCl100k = "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"
	urlO200k  = "https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken"
)

type embeddedBpeLoader struct{}

func (l *embeddedBpeLoader) LoadTiktokenBpe(blobpath string) (map[string]int, error) {
	var data []byte
	switch blobpath {
	case urlCl100k:
		data = cl100kBaseData
	case urlO200k:
		data = o200kBaseData
	default:
		return nil, fmt.Errorf("no embedded BPE file for %s", blobpath)
	}
	return parseBpeRanks(data)
}

func parseBpeRanks(data []byte) (map[string]int, error) {
	ranks := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed BPE line: %q", line)
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, fmt.Errorf("decode BPE token: %w", err)
		}
		rank, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse BPE rank: %w", err)
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}

func init() {
	tiktoken.SetBpeLoader(&embeddedBpeLoader{})
}
