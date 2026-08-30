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
	VideoReference  bool
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

Use the exact identifiers required by the selected structure. In Ref2VA, define a human as <Subject 1>; never replace it with an alias such as <Adult Woman>. In detailed_description, include explicit chronological time ranges that cover the requested duration and reserve the final interval for a stable hold.

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

// SystemPromptWithVideoContextAndReferences keeps the video workflow contract
// and the user-assigned role of every attached picture in the same request.
func SystemPromptWithVideoContextAndReferences(mode Mode, profile Profile, references []ImageReference, context VideoContext) string {
	return systemPrompt(mode, profile, references, context)
}

func systemPrompt(mode Mode, profile Profile, references []ImageReference, video VideoContext) string {
	if mode == ModeTextToVideo && profile == ProfileMiniMaxH3 {
		prompt := miniMaxH3Instruction + "\n\n" + miniMaxH3FormatInstruction(video)
		prompt += miniMaxH3ReferenceMapInstruction(references, video)
		prompt += minorSafetyInstruction
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

func miniMaxH3ReferenceMapInstruction(references []ImageReference, context VideoContext) string {
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
		if context.Mode == "references" {
			lines = append(lines, fmt.Sprintf("- <Picture %d> (attached image %d): %s", reference.Number, reference.Number, referenceRoleInstruction(reference.Role)))
			continue
		}
		frameRole := "the exact opening frame"
		if reference.Number == 2 {
			frameRole = "the exact final frame"
		}
		lines = append(lines, fmt.Sprintf("- <Picture %d> (attached image %d): %s", reference.Number, reference.Number, frameRole))
	}
	if len(lines) == 0 {
		return ""
	}
	if context.Mode != "references" {
		return "\n\nThe attached keyframes are:\n" + strings.Join(lines, "\n") + `

Inspect every attached keyframe before writing. Ground the opening in the exact visible subject, clothing, pose, framing, setting, lighting, and composition of <Picture 1>. When <Picture 2> is supplied, make the action reach its exact visible subject state, pose, framing, setting, lighting, and composition at the final moment. Do not treat either keyframe as a loose style reference and do not invent a visual detail that contradicts it.`
	}
	return "\n\nThe attached pictures and their declared roles are:\n" + strings.Join(lines, "\n") + `

Inspect every attached picture before writing. In retention_analysis, identify each supplied <Picture N> separately and state only the visible source details relevant to its declared role. Give at least three concrete visible attributes for every picture. For a person or base scene, use relevant facts such as hair, visible clothing and color, pose or framing, named room anchors, and lighting. For a product or object, use its silhouette, material, color, construction, and legible branding when visible. A generic restatement such as "an adult woman in a room" or "the referenced product" is invalid. Carry the concrete retained attributes into subject_definitions and detailed_description so the generated action remains grounded in the actual pictures. Never attribute a color, garment, object, person, pose, or background from one picture to another, and do not confuse the user's requested final state with what is visibly present in a source picture.`
}

func miniMaxH3FormatInstruction(context VideoContext) string {
	duration := context.DurationSeconds
	if duration < 5 || duration > 60 {
		duration = 5
	}
	imageCount := context.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	if context.Mode == "references" {
		audioReference := "No standalone audio reference is attached; do not use an <Audio N> identifier."
		if context.AudioReference {
			audioReference = "One standalone <Audio 1> reference is attached and will be passed to MiniMax. When the user assigns it as a voice, music, or ambience reference, bind that exact role to <Audio 1> in the prompt. Never claim to hear, identify, or transcribe details that the user did not provide."
		}
		videoReference := "No <Video 1> reference is attached; do not use a <Video N> identifier."
		if context.VideoReference {
			videoReference = "One <Video 1> reference is attached and will be passed to MiniMax. Use the exact <Video 1> identifier when the user's request assigns it a motion, scene, camera, or timing role; never claim to inspect details that are not supplied in the user's text."
		}
		retentionInstruction := "Write N/A - no reference media supplied. Do not invent <Picture N>, <Video N>, or <Audio N> identifiers."
		if imageCount > 0 || context.AudioReference || context.VideoReference {
			retentionInstruction = "State separately what every supplied <Picture N>, <Video 1>, and <Audio 1> contributes. Use only identifiers for media that is actually attached."
		}
		return fmt.Sprintf(`The selected mode is prompt-driven Ref2VA with %d attached image reference(s). Reference files are optional; this workflow remains valid with no media. Use exactly this structure:

subject_definitions:
Define the important subjects required by the user's request. When a person is present, identify the first person as <Subject 1> and ground visible attributes in the supplied pictures.

summary:
[reference generation] one concise chronological summary.

retention_analysis:
%s %s %s

detailed_description:
Write the full %d-second chronological shot description.

overall_soundscape:
Describe ambience and synchronized physical sounds only.

non_diegetic_music:
N/A unless requested.`, imageCount, retentionInstruction, audioReference, videoReference, duration)
	}
	if imageCount >= 2 {
		return fmt.Sprintf(`The selected mode is FL2VA. Picture 1 is the exact opening frame and Picture 2 is the exact final frame. Begin exactly with:

How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video; Picture 2 (from Shot 1) aligns with the %d.00-second mark of the target video.

Then write these fields in order:
integrated_multimodal_description: a complete chronological path that reaches Picture 2 exactly.
overall_soundscape: ambience and synchronized physical sounds only.
non_diegetic_music: N/A unless requested.`, duration)
	}
	if imageCount == 0 {
		return fmt.Sprintf(`The selected mode is T2VA. There is no opening or closing picture. Do not use <Picture N> identifiers. Write these fields in order:

integrated_multimodal_description: a complete chronological %d-second video described purely from the user's text, with a stable opening anchor, continuous physically coherent action, and a final hold.
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
