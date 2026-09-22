package tokenizer

import (
	"testing"

	"github.com/pkoukk/tiktoken-go"
)

func TestParseBpeRanks(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		minRows int
		wantErr bool
	}{
		{"cl100k_base", cl100kBaseData, 100000, false},
		{"o200k_base", o200kBaseData, 190000, false},
		{"empty", []byte{}, 0, false},
		{"malformed", []byte("badline\n"), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranks, err := parseBpeRanks(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(ranks) < tt.minRows {
				t.Fatalf("expected >= %d ranks, got %d", tt.minRows, len(ranks))
			}
		})
	}
}

func TestEmbeddedBpeLoaderKnownURLs(t *testing.T) {
	l := &embeddedBpeLoader{}
	for _, url := range []string{urlCl100k, urlO200k} {
		ranks, err := l.LoadTiktokenBpe(url)
		if err != nil {
			t.Fatalf("LoadTiktokenBpe(%s): %v", url, err)
		}
		if len(ranks) < 100000 {
			t.Fatalf("LoadTiktokenBpe(%s): too few ranks (%d)", url, len(ranks))
		}
	}
}

func TestEmbeddedBpeLoaderUnknownURL(t *testing.T) {
	l := &embeddedBpeLoader{}
	_, err := l.LoadTiktokenBpe("https://example.com/unknown.tiktoken")
	if err == nil {
		t.Fatal("expected error for unknown URL, got nil")
	}
}

func TestEmbeddedLoaderIsRegistered(t *testing.T) {
	enc, err := tiktoken.GetEncoding(tiktoken.MODEL_O200K_BASE)
	if err != nil {
		t.Fatalf("GetEncoding(o200k_base): %v", err)
	}
	if enc == nil {
		t.Fatal("expected non-nil encoder")
	}
	if n := len(enc.EncodeOrdinary("hello world")); n != 2 {
		t.Fatalf("expected 2 tokens for 'hello world', got %d", n)
	}
}

func TestForModelGPT4o(t *testing.T) {
	tke := ForModel("gpt-4o")
	if tke == nil {
		t.Fatal("expected non-nil tokenizer for gpt-4o")
	}
	if n := len(tke.EncodeOrdinary("hello world")); n != 2 {
		t.Fatalf("expected 2 tokens for 'hello world', got %d", n)
	}
}

func TestForModelGPT4(t *testing.T) {
	tke := ForModel("gpt-4")
	if tke == nil {
		t.Fatal("expected non-nil tokenizer for gpt-4")
	}
	if n := len(tke.EncodeOrdinary("hello world")); n != 2 {
		t.Fatalf("expected 2 tokens for 'hello world', got %d", n)
	}
}

func TestCountKnownValues(t *testing.T) {
	tests := []struct {
		model string
		text  string
		want  int
	}{
		{"gpt-4o", "hello world", 2},
		{"gpt-4", "hello world", 2},
		{"gpt-4o", "", 0},
		{"gpt-4", "", 0},
	}
	for _, tt := range tests {
		got := Count(tt.model, tt.text)
		if got != tt.want {
			t.Errorf("Count(%q, %q) = %d, want %d", tt.model, tt.text, got, tt.want)
		}
	}
}

func TestCountCJK(t *testing.T) {
	n := Count("gpt-4o", "你好世界")
	if n <= 0 {
		t.Fatalf("expected positive token count for CJK text, got %d", n)
	}
}
