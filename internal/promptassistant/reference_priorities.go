package promptassistant

import (
	"fmt"
	"strings"
)

// Keep selected roles authoritative when several pictures show the same person.
// Keyframes instead preserve the complete visible state of each endpoint.
func referenceSourcePriorityInstruction(mode Mode, references []ImageReference, video VideoContext) string {
	if len(references) < 2 || mode == ModeTextToVideo && video.Mode == VideoModeFL2VA {
		return ""
	}
	var identity, wardrobe []string
	for _, reference := range references {
		id := fmt.Sprintf("Picture %d", reference.Number)
		switch reference.Role {
		case ImageReferenceIdentity:
			identity = append(identity, id)
		case ImageReferenceWardrobeObject:
			wardrobe = append(wardrobe, id)
		}
	}
	if len(identity) == 0 || len(wardrobe) == 0 {
		return ""
	}
	return fmt.Sprintf(`

Selected source priorities:
Identity source(s): %s. Wardrobe/object source(s): %s.
Inspect and describe each source separately in references BEFORE composing the prompt field. Do not reuse the first picture's description for another picture. For the wardrobe/object source, name the actually visible garment or object, its color, material and shape when discernible. If a detail is unclear, say so instead of borrowing it from the identity source.
When combining these references, retain the face and hair from the identity source and the requested clothing or object from the wardrobe/object source. Clothing visible in an identity source is not the target outfit. A request to animate the person from the first photo does not cancel the selected wardrobe role. Only an explicit contrary user instruction changes that priority.
Before returning, check that the final subject description uses the selected wardrobe source, not the outfit in the identity photo or video. Do not mix outfits or describe alternative outfits unless explicitly requested.
`, strings.Join(identity, ", "), strings.Join(wardrobe, ", "))
}
