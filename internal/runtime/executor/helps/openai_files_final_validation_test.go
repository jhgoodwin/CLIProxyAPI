package helps

import "testing"

func TestOpenAIFilePlacementRejectsMovedAndDuplicatedReferences(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"file_id":"file-owned"}]}`,
		`{"input":[{"type":"function_call_output","output":[{"type":"input_image","file_id":"file-owned"}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-owned","image_url":"https://example.test/x"}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-owned"}]}],"tools":[{"file_id":"file-owned"}]}`,
	} {
		if err := ValidateOpenAIFilePlacement([]byte(body)); err == nil {
			t.Fatal("invalid placement accepted", body)
		}
	}
	for _, body := range []string{`{"input":[]}`, `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-owned","detail":"auto"}]}]}`} {
		if err := ValidateOpenAIFilePlacement([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}
