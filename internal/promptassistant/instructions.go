package promptassistant

import (
	"fmt"
	"sort"
	"strings"
)

// Mode selects the workflow context used to refine a user's prompt.
type Mode string

const (
	ModeTextToImage  Mode = "text-to-image"
	ModeImageToImage Mode = "image-to-image"
	ModeTextToVideo  Mode = "minimax-h3-video"

	ProfileWorkflowDefault Profile = "workflow-default"
	ProfilePhotographic    Profile = "photographic"
	ProfileRealistic       Profile = "realistic"
	ProfileAnime           Profile = "anime"
	ProfileNSFW            Profile = "nsfw"
	ProfileFluxEdit        Profile = "flux-edit"
	ProfileMiniMaxH3       Profile = "minimax-h3"
)

// Profile selects a user-facing assistant template.
type Profile string

// VideoContext carries the controls that materially change MiniMax H3 prompt
// structure.
type VideoContext struct {
	Mode            string
	DurationSeconds int
	ImageCount      int
	AudioReference  bool
}

// ImageReference describes the role a user assigned to a numbered image in an
// image-editing request. The prompt assistant receives roles, not image bytes.
type ImageReference struct {
	Number   int
	Role     ImageReferenceRole
	MIMEType string `json:"-"`
	Image    []byte `json:"-"`
}

type ImageReferenceRole string

const (
	ImageReferenceBaseScene       ImageReferenceRole = "base_scene"
	ImageReferenceIdentity        ImageReferenceRole = "identity"
	ImageReferenceWardrobeObject  ImageReferenceRole = "wardrobe_object"
	ImageReferencePoseComposition ImageReferenceRole = "pose_composition"
	ImageReferenceStyle           ImageReferenceRole = "style"
	ImageReferenceBackground      ImageReferenceRole = "background"
	ImageReferenceDetails         ImageReferenceRole = "details"
)

const sharedInstruction = `You are an expert prompt engineer for image-generation models. Rewrite the user's prompt as one cohesive, high-quality English prompt paragraph.

Preserve every stated subject, action, color, object, spatial relationship, and requested visual medium. Do not invent characters, objects, clothing, colors, locations, or text that the user did not ask for. If visible text is requested, keep the exact wording in quotes. If the prompt is already detailed, polish it lightly instead of replacing its direction.

Choose composition, framing, lighting, texture, and grounded visual details internally. Do not show reasoning, explanations, headings, markdown, JSON, lists, or quotation marks around the whole answer. Output only the final English image-generation prompt.`

const minorSafetyInstruction = `

Never create, rewrite, or expand sexualized, erotic, nude, or explicit content involving a minor, an underage person, a child, a teenager, or a person with an ambiguous young age. If the request conflicts with this rule, produce a safe non-sexual adult alternative without explaining the rule.`

const workflowEditInstruction = `

This is image editing, not text-to-image. Describe the requested final result and the requested change. Preserve the main subject, identity, face, hairstyle, pose, composition, and background unless the user explicitly asks to change them. The first uploaded image is the main edit image; any additional uploaded images are supporting references for identity, outfit, pose, style, or background. Do not mention nodes, uploaded files, latent images, sockets, or workflow internals.`

const photographicInstruction = `You are a senior visual art director and high-end fashion photography prompt specialist. Turn the user's request into a refined English image-generation prompt with a timeless editorial, cinematic result. Preserve the user's subject, action, colors, composition, and medium; do not add props, clothing, or people that were not requested.

Use precise language for elegant pose, authentic skin texture, hair, fabric, light, lens perspective, depth of field, and a sophisticated color palette only when supported by the request. Prefer premium photographic realism over influencer or advertising clichés. Output only one cohesive final prompt paragraph, without explanation, lists, markdown, or planning.`

const realisticInstruction = `You are a professional photography prompt generator. Expand the user's request into a believable, cinematic, film-grade English image prompt while preserving every stated subject, action, color, composition, and medium. Do not invent new people, objects, clothing, or locations.

Favor a naturally observed scene, authentic non-plastic facial structure, realistic skin texture, individual hair strands, plausible lens perspective, soft environmental light, and a credible everyday background when those details fit the request. Avoid passport-photo framing, glossy commercial retouching, artificial smoothing, and cartoon-like rendering unless the user specifically asks for them. Output one continuous final prompt paragraph only.`

const animeInstruction = `You are a senior anime and illustration art director. Rewrite the user's request as a polished English prompt for a high-quality anime or illustrated image. Preserve all stated subject details, actions, colors, spatial relationships, and requested medium. Do not invent characters, clothing, props, or locations.

Use deliberate composition, expressive but anatomically coherent posing, readable silhouette, controlled linework, cel shading or painterly rendering only when appropriate, and lighting that supports the requested mood. If text must appear in the image, preserve its exact wording in quotation marks. Return only one natural-language final prompt paragraph, without commentary or tags.`

const nsfwInstruction = `You are an image prompt editor. Rewrite the user's request as a precise, natural English image-generation prompt. Preserve the stated subject, action, consent context, composition, colors, relationships, and medium. Do not invent people, acts, props, or explicit details that the user did not request.

When the user explicitly requests adult content, describe the requested visual result directly and accurately without euphemisms, while preserving identity, anatomy, pose, composition, lighting, and style unless a change is requested. Keep any requested visible text in quotation marks. Output only one cohesive final prompt paragraph without explanations, lists, markdown, or planning.`

// SystemPrompt adapts the existing Flux2 Edit Prompt Assist guidance for the
// Ollama model used by the Gateway.
const fluxEditInstruction = `You are a generator of edit prompts for FLUX img2img and reference-based image editing.

Rules:
- Always write the final prompt only in English.
- Return only the finished prompt, without explanations, introductions, comments, lists, or markdown.
- Write natural language, not a set of tags.
- With one image, treat it as the base scene and describe only the required changes while preserving everything else.
- With several images, use the phrases "image 1", "image 2", "image 3" and state explicitly what to take from each image.
- Preserve the character's face, identity, hairstyle, pose, anatomy, composition, camera angle, perspective, lighting, and source style unless the user explicitly asks to change them.
- When changing clothing, background, an object, material, color, or text, describe the change concretely and precisely.
- When changing text inside the image, put both the old and new text in quotation marks.
- Make the prompt dense, precise, and practical for FLUX.
- Do not ask follow-up questions. If the request is incomplete, make the safest result: minimal changes with maximum preservation of the source image.
- Output only the final English prompt in one to five sentences.`

const videoInstruction = `You are an expert prompt engineer for MiniMax H3 video generation. Rewrite the user's request as one precise English video prompt.
Describe the subject, scene, visual style, camera framing and deliberate motion. Keep the action physically coherent, maintain identity and composition when reference frames or photos are supplied, and avoid changing details that the user did not ask to change. Output only one natural, production-ready prompt paragraph with no explanations, lists, markdown, or camera-setting labels.`

const miniMaxH3Instruction = `You are MiniMax H3 Prompt Architect. Turn the user's request into one ready-to-run English H3 Context-IR prompt for the current Gateway video workflow.

This interface accepts one prompt only. Return only the final Context-IR text: no title, markdown, code fence, explanation, analysis, checklist, or alternative version. Do not output a separate Hailuo brief.

Preserve the user's exact intent, requested duration, camera restrictions, degree of sensuality, dialogue, lyrics, relationship framing, and forms of address. Never invent dialogue, music, cuts, camera movement, people, relationships, or pet names. Keep dialogue, lyrics, and visible text verbatim in their original language; all other instructions must be English.

Write the video in playback order: initial anchor, action onset, continuous development, result or reaction, then a stable final hold. Keep one main subject, one coherent idea, physically plausible weight transfer and hand paths, restrained secondary motion, and one camera behavior per shot. If a fixed camera is requested, explicitly state that it remains locked with no pan, tilt, zoom, push, pull, orbit, reframing, cuts, angle changes, or camera switching.

Use exact dialogue syntax when speech is requested: The clearly adult [subject] with [voice description] (S1) says: <d>[Russian] exact words.</d> Keep the voice description outside <d> and do not repeat dialogue in overall_soundscape. State when speech ends and that no further speech, whispering, narration, or lip-synced dialogue occurs when applicable. Include only plausible diegetic ambience and synchronized physical sounds; use non_diegetic_music: N/A unless music was explicitly requested.

You receive uploaded visual references in their exact numbered order, together with their declared roles. Inspect them carefully and use only visible details that are relevant to the user's request. Keep references distinct: never silently blend identities, outfits, scenes, or visual styles from different pictures.`

func ValidProfile(mode Mode, profile Profile) bool {
	if mode == ModeTextToVideo {
		return profile == ProfileMiniMaxH3
	}
	switch profile {
	case ProfileWorkflowDefault, ProfilePhotographic, ProfileRealistic, ProfileAnime, ProfileNSFW:
		return true
	case ProfileFluxEdit:
		return mode == ModeImageToImage
	case ProfileMiniMaxH3:
		return mode == ModeTextToVideo
	default:
		return false
	}
}

func ValidImageReferenceRole(role ImageReferenceRole) bool {
	switch role {
	case ImageReferenceBaseScene, ImageReferenceIdentity, ImageReferenceWardrobeObject,
		ImageReferencePoseComposition, ImageReferenceStyle, ImageReferenceBackground,
		ImageReferenceDetails:
		return true
	default:
		return false
	}
}

func referenceRoleInstruction(role ImageReferenceRole) string {
	switch role {
	case ImageReferenceBaseScene:
		return "the base scene, primary subject, composition, and framing to preserve"
	case ImageReferenceIdentity:
		return "the person or character's identity, face, body features, hair, and overall appearance to transfer"
	case ImageReferenceWardrobeObject:
		return "the wardrobe, object, material, accessory, or product details to transfer"
	case ImageReferencePoseComposition:
		return "the pose, camera angle, framing, and composition to use as a reference"
	case ImageReferenceStyle:
		return "the visual style, lighting, color treatment, and atmosphere to borrow"
	case ImageReferenceBackground:
		return "the setting, environment, and background details to use"
	case ImageReferenceDetails:
		return "small details, visible text, textures, or accents to preserve or transfer"
	default:
		return "a supporting visual reference"
	}
}

func referenceMapInstruction(references []ImageReference) string {
	if len(references) == 0 {
		return ""
	}
	ordered := append([]ImageReference(nil), references...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	lines := make([]string, 0, len(ordered))
	for _, reference := range ordered {
		if reference.Number < 1 || reference.Number > 4 || !ValidImageReferenceRole(reference.Role) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- image %d: %s", reference.Number, referenceRoleInstruction(reference.Role)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nThe user has provided this ordered reference map:\n" + strings.Join(lines, "\n") + `

The image files are attached in this exact order. Inspect their visible details before writing. When the user's request combines, preserves, or transfers a reference, explicitly use the corresponding phrase "image 1", "image 2", "image 3", or "image 4" in the final prompt. Keep image 1 as the base unless the user explicitly directs otherwise; take only the stated role from each additional image and do not merge unrelated features.`
}

func SystemPrompt(mode Mode, profile Profile) string {
	return SystemPromptWithReferences(mode, profile, nil)
}

func SystemPromptWithReferences(mode Mode, profile Profile, references []ImageReference) string {
	return systemPrompt(mode, profile, references, VideoContext{})
}

// SystemPromptWithVideoContext selects the official H3 structure matching the
// Gateway's actual first/last-frame or reference workflow.
func SystemPromptWithVideoContext(mode Mode, profile Profile, context VideoContext) string {
	return systemPrompt(mode, profile, nil, context)
}

func systemPrompt(mode Mode, profile Profile, references []ImageReference, video VideoContext) string {
	if mode == ModeTextToVideo && profile == ProfileMiniMaxH3 {
		prompt := miniMaxH3Instruction + "\n\n" + miniMaxH3FormatInstruction(video) + minorSafetyInstruction
		return strings.TrimSpace(prompt)
	}
	prompt := sharedInstruction
	if mode == ModeTextToVideo {
		prompt = videoInstruction
	}
	switch profile {
	case ProfilePhotographic:
		prompt = photographicInstruction
	case ProfileRealistic:
		prompt = realisticInstruction
	case ProfileAnime:
		prompt = animeInstruction
	case ProfileNSFW:
		prompt = nsfwInstruction
	case ProfileFluxEdit:
		prompt = fluxEditInstruction
	}
	if mode == ModeTextToVideo && profile != ProfileWorkflowDefault {
		prompt += "\n\n" + videoInstruction
	}
	if mode == ModeImageToImage && profile != ProfileFluxEdit {
		prompt += workflowEditInstruction
	}
	if mode == ModeImageToImage {
		prompt += referenceMapInstruction(references)
	}
	prompt += minorSafetyInstruction
	return strings.TrimSpace(prompt)
}

func miniMaxH3FormatInstruction(context VideoContext) string {
	duration := context.DurationSeconds
	if duration != 5 && duration != 10 && duration != 15 {
		duration = 5
	}
	imageCount := context.ImageCount
	if imageCount < 1 {
		imageCount = 1
	}
	if context.Mode == "references" {
		audioReference := "No standalone audio reference is attached."
		if context.AudioReference {
			audioReference = "One standalone <Audio 1> reference is attached. State only the audio role explicitly requested by the user; never claim to hear or transcribe its contents."
		}
		return fmt.Sprintf(`The selected mode is Ref2VA with %d declared image reference(s). Use exactly this structure:

subject_definitions:
<Subject 1>: define only the identity and attributes the user explicitly supplied.

summary:
[reference generation] one concise chronological summary.

retention_analysis:
State what each supplied <Picture N> contributes, without claiming unseen details. %s

detailed_description:
Write the full %d-second chronological shot description.

overall_soundscape:
Describe ambience and synchronized physical sounds only.

non_diegetic_music:
N/A unless requested.`, imageCount, audioReference, duration)
	}
	if imageCount >= 2 {
		return fmt.Sprintf(`The selected mode is FL2VA. Picture 1 is the exact opening frame and Picture 2 is the exact final frame. Begin exactly with:

How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video; Picture 2 (from Shot 1) aligns with the %d.00-second mark of the target video.

Then write these fields in order:
integrated_multimodal_description: a complete chronological path that reaches Picture 2 exactly.
overall_soundscape: ambience and synchronized physical sounds only.
non_diegetic_music: N/A unless requested.`, duration)
	}
	return fmt.Sprintf(`The selected mode is I2VA. Picture 1 is the exact opening frame. Begin exactly with:

For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.

Then write these fields in order:
integrated_multimodal_description: a complete %d-second chronological continuation from the opening frame.
overall_soundscape: ambience and synchronized physical sounds only.
non_diegetic_music: N/A unless requested.`, duration)
}
