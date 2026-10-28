package voltkit

import (
	"reflect"
	"strings"
	"testing"
)

// The Music add-on in GetRates (0.1.2): Addons.Music is kept whole, typed like
// a tier row, and a price list from before the add-on decodes as it did.
func TestRequest_RatesCarryTheMusicAddon(t *testing.T) {
	music := map[string]any{
		"id": "music", "label": "Music add-on", "price": "10.00", "cuPerParticipantSecond": "1000",
		"unit":       "credits per 1,000 participant-minutes while music plays",
		"appliesTo":  []string{"audio_std", "audio_hd"},
		"includedIn": []string{"video_30", "video_480_60", "video_720_60", "video_1080_60"},
	}
	ft := scripted(jsonStep(200, map[string]any{"success": true, "data": map[string]any{"version": 4, "addons": map[string]any{"music": music}}}))
	rates, err := newClient(t, ft, withRates("https://api.example.com")).GetRates(ctxBG)
	if err != nil {
		t.Fatal(err)
	}
	if rates.Addons == nil || rates.Addons.Music == nil {
		t.Fatalf("the Music add-on was dropped: %+v", rates)
	}
	m := rates.Addons.Music
	if m.ID != "music" || m.Label != "Music add-on" || m.Price != "10.00" || m.CuPerParticipantSecond != "1000" || !strings.Contains(m.Unit, "while music plays") {
		t.Errorf("music add-on %+v", m)
	}
	if !reflect.DeepEqual(m.AppliesTo, []string{"audio_std", "audio_hd"}) {
		t.Errorf("billed on the voice tiers: appliesTo %v", m.AppliesTo)
	}
	if !reflect.DeepEqual(m.IncludedIn, []string{"video_30", "video_480_60", "video_720_60", "video_1080_60"}) {
		t.Errorf("included with video: includedIn %v", m.IncludedIn)
	}
	// An exact cost while music plays, like a tier's: 4 people for 30 minutes.
	cu, err := ParseCu(m.CuPerParticipantSecond)
	if err != nil || cu.Int64()*4*30*60 != 7_200_000 {
		t.Errorf("cost: %v %v", cu, err)
	}
	before := scripted(jsonStep(200, map[string]any{"success": true, "data": map[string]any{"version": 3}}))
	older, err := newClient(t, before, withRates("https://api.example.com")).GetRates(ctxBG)
	if err != nil || older.Addons != nil {
		t.Errorf("a price list without addons: %+v, %v", older, err)
	}
}
