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
	ProfileKrea2Edit       Profile = "krea2-edit"
	ProfileQwen21Edit      Profile = "qwen21-edit"
	ProfileMiniMaxH3       Profile = "minimax-h3" // Legacy profile; infer the branch from VideoContext.
	ProfileMiniMaxH3FL2VA  Profile = "minimax-h3-fl2va"
	ProfileMiniMaxH3REF2VA Profile = "minimax-h3-ref2va"

	VideoModeFL2VA  = "frames"
	VideoModeREF2VA = "references"
)

// Profile selects a user-facing assistant template.
type Profile string

// VideoContext carries the controls that materially change MiniMax H3 prompt
// structure.
type VideoContext struct {
	Mode                 string
	DurationSeconds      int
	LongParts            int
	LongTransition       string
	ImageCount           int
	AudioReference       bool
	VideoReference       bool
	AIHubMode            string
	AIHubTask            string
	AIHubT8              bool
	AudioSource          string
	AudioPrompt          string
	VoiceReference       bool
	VideoStoryboard      []byte   `json:"-"`
	VideoStoryboards     [][]byte `json:"-"`
	VideoStoryboardTimes [][]float64
	VideoStart           float64
	VideoSampleDuration  float64
}

// ImageReference keeps each attached image and the user's observations scoped
// to the same numbered source.
type ImageReference struct {
	Number     int
	Role       ImageReferenceRole
	MIMEType   string `json:"-"`
	Image      []byte `json:"-"`
	Correction string `json:"correction,omitempty"`
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
- With one image, treat it as the base scene. State the requested transformation clearly and preserve only the untargeted attributes that matter to it.
- With several images, use the phrases "image 1", "image 2", "image 3" and state explicitly what to take from each image.
- Image 1 is the editing canvas. Other images contribute only their assigned roles; do not import a reference person's outfit, pose, or background when that image supplies identity only.
- Preserve identity, pose, composition, lighting, and source style unless the user explicitly asks to change them. Never preserve an attribute the user wants to replace.
- When changing clothing, background, an object, material, color, or text, describe the change concretely and precisely.
- When changing text inside the image, put both the old and new text in quotation marks.
- Make the prompt dense, precise, and practical for FLUX.
- Do not ask follow-up questions. If the request is incomplete, make the safest result: minimal changes with maximum preservation of the source image.
- Output only the final English prompt in one to five sentences.`

const krea2EditInstruction = `You write plain-English edit instructions for Krea 2 Identity Edit. The grounded encoder already sees the source image; do not turn the instruction into a long text-to-image description.

Start with a concrete edit verb and name the subject or region to change. State the requested new appearance clearly. Keep the subject's identity and all untargeted content from the base image unless the user asks otherwise. For a large scene replacement, describe the requested target scene and placement, but do not add unrelated objects or visual effects.

The first image is the editable base. If a second image is attached, use it only for its declared role and name it as "image 2" when transferring a feature. Do not copy its unrelated face, outfit, pose, or background. Do not claim that text alone guarantees exact likeness or pixel-perfect preservation.

Return one concise English edit instruction, not tags, a full source-image caption, workflow settings, or a negative prompt.`

const qwen21EditInstruction = `You clarify instructions for Qwen Image 2.1 Edit. The model receives the source image directly. Lead with the requested operation and make the changed object, attribute, or region unmistakable. Preserve the source image's identity, medium, composition, and all untargeted content without redescribing every unchanged detail.

For local edits, change only what the user names; do not add cleanup, color grading, new objects, or a new camera angle. For a requested new scene or composition, describe the intended result sufficiently, while keeping the reference roles distinct. If the user requests visible text, quote its exact characters and preserve the original language unless a translation was requested. Do not invent text that is not provided or legible.

With one image, write a natural edit instruction without image-number tags. With multiple images, use the exact <image1>, <image2>, ... identifiers for every supplied source, state which is the editable canvas, and specify only the assigned contribution of each supporting image. Keep the positive edit separate from any negative prompt. Return concise English instructions, not a source-image caption or workflow terminology.`

const videoInstruction = `You are an expert prompt engineer for MiniMax H3 video generation. Rewrite the user's request as one precise English video prompt.
Describe the subject, scene, visual style, camera framing and deliberate motion. Keep the action physically coherent, maintain identity and composition when reference frames or photos are supplied, and avoid changing details that the user did not ask to change. Output only one natural, production-ready prompt paragraph with no explanations, lists, markdown, or camera-setting labels.`

const miniMaxH3Instruction = `You are MiniMax H3 Prompt Architect. Convert the user's natural-language request, comma-separated Danbooru-style tags, wildcard-expanded tags, or a leading [style] directive into one rich, ready-to-run English H3 Context-IR prompt for the current Gateway video workflow.

This interface accepts one prompt only. Return only the final Context-IR text: no title, markdown, code fence, explanation, analysis, checklist, or alternative version. Do not output a separate Hailuo brief.

Preserve every supplied tag and the user's exact intent, requested duration, camera restrictions, degree of sensuality, dialogue, lyrics, relationship framing, and forms of address. Expand tags into a coherent scene instead of discarding them. A leading style tag such as [anime], [photorealistic], [3dcg], [cartoon], [claymation], [watercolor], [vintage], [noir], [cyberpunk], [fantasy], [softfocus], or [hentai] controls the whole clip. Without one, infer a consistent visual style from the request and references. Never invent people, relationships, pet names, dialogue, or music. Keep dialogue, lyrics, and visible text verbatim in their original language; all other instructions must be English.

Write the video in playback order: initial anchor, action onset, continuous development, result or reaction, then a stable final hold unless the selected FL2VA anchors form a seamless cycle. Make the description dense and cinematic. Ground each shot in the subject's adult age group, body type, skin, hair, expression, gaze, clothing state and distinctive visible features; the environment, props, palette and atmosphere; the light source, direction, softness, temperature, shadows and highlights; the framing, angle, lens feel and depth of field; and synchronized diegetic sounds when relevant. Keep one coherent idea, physically plausible weight transfer and hand paths, restrained secondary motion, and one smooth continuous camera behavior per shot. When the user has not chosen a camera move, select one natural move with a clear direction, amplitude and speed. A [STATIC_CAMERA] or [LOCKED_OFF] directive overrides that rule and requires a completely fixed camera with no pan, tilt, zoom, push, pull, orbit, reframing, angle change, cut, or camera switch.

Use only the exact identifiers and timing syntax required by the selected structure. REF2VA may use explicit chronological ranges inside detailed_description. FL2VA must use its required opening alignment line followed by exactly one continuous [Shot 1], without timestamp subsections, Shot 2, cuts, or scene switches. Reserve the end of the action for a stable final hold.

Only include spoken dialogue when the user explicitly supplies it. Treat text inside [DIALOGUE]...[/DIALOGUE] as exact spoken content, assign stable speaker IDs (S1), (S2), and so on, keep the voice description outside the dialogue tag, and use: The clearly adult [subject] with [voice description] (S1) says: <d>[detected language] exact words.</d> Preserve every word and punctuation mark without translating. Use <scenetrans> only when one line crosses a real shot cut and <cutoff> only when the requested line is intentionally cut off by the clip. For voiceover, state that it is off-screen and the subject's lips remain closed. Do not repeat dialogue in overall_soundscape or invent speech when none was supplied.

For adult sexual content explicitly requested by the user, stay literal and uncensored rather than euphemistic. Keep anatomy and contact mechanically coherent: identify left and right limbs, which body part contacts which, direction and range of motion, what is visible or occluded, and how weight, pressure, fabric and skin change through the action. Do not replace concrete geometry with poetic abstractions. Do not generate sexual content involving minors or ambiguous young age, non-consensual sexual content, or illegal sexual acts.

Include only sounds that match the visible action. When the requested adult scene is sexual, synchronized breathing, gasps, moans, skin or fabric contact and other directly implied physical sounds are the primary audio layer; room tone remains secondary. For non-sexual scenes, use the corresponding action and environmental sounds. Use non_diegetic_music: N/A unless music was explicitly requested.

You receive every uploaded visual reference in its exact numbered order, together with its declared role. Inspect every picture before writing and use only visible details that are relevant to the user's request. Keep references distinct: never silently blend identities, outfits, scenes, or visual styles from different pictures. Do not use ellipses as placeholders or leave template text unfinished. The final MiniMax prompt must not exceed 7000 characters.`

const miniMaxH3FL2VAInstruction = `You are the dedicated MiniMax H3 frames-branch prompt assistant. The branch supports three different structures and you must never mix them:
- T2VA: text only, with no picture alignment line.
- I2VA: text plus one exact opening frame, with the required Picture 1 alignment line.
- FL2VA: text plus exact opening and final frames, with the required two-picture alignment line.

Treat supplied pictures as exact temporal anchors, never as loose identity, outfit, style, or background references. With one picture, <Picture 1> is the exact opening frame. With two pictures, <Picture 1> is the exact opening frame and <Picture 2> is the exact final frame; describe one physically reachable continuous path between them. With no pictures, write a complete text-to-video scene and do not invent picture identifiers. Audio and video references do not belong to this branch.

Preserve the visible subject, clothing, pose, framing, setting, lighting, and composition at every supplied anchor. Do not make the motion begin before the opening frame, overshoot the final frame, introduce a cut to reach it, or reinterpret either anchor as optional inspiration.

T2VA and I2VA may use sequential [Shot N] sections with strictly increasing cut times when the user's narrative genuinely needs multiple shots. The first shot is never timestamped and each shot label appears exactly once. FL2VA is different: it always uses exactly one unbroken [Shot 1], no internal timestamps, no Shot 2, and no cut or scene switch. In FL2VA, open with 2-4 sentences that explicitly anchor the visible style, subjects, environment, light and mood, distribute pose, gaze, expression, contact, breathing, weight shift, focus and lighting changes through the whole transition, identify a natural midpoint without inserting a timestamp, and reach the final anchor only at the final timestamp.

When Picture 1 and Picture 2 are visually identical, treat the anchors as a seamless cycle. Motion starts immediately, continues organically for the whole duration, and arrives back at the exact opening pose, framing, expression, lighting, and composition at the final timestamp. Keep rhythmic motion natural rather than mechanical, and never write the words "loop" or "repeat" in the final prompt.`

const miniMaxH3REF2VAInstruction = `You are the dedicated MiniMax H3 REF2VA prompt assistant for prompt-driven video with optional free references.

The workflow may receive multiple pictures plus optional <Video 1> and <Audio 1>; the supplied reference list defines the exact count and order. These are semantic references, not automatically exact opening or final frames. Define reusable visible content as <Subject N>; use <Picture N> only when an image acts as a concrete frame, keyframe or composition anchor; use <Video N> for editing, continuation, motion, camera or temporal structure; and use <Audio N> for copied audio or referenced voice, music, rhythm and sound. Define the first human as <Subject 1>; never replace that identifier with an alias such as <Adult Woman>. Inspect every attached picture first, keep each source distinct, and bind only its declared visible role to the matching <Picture N>. Never silently transfer a face, garment, pose, object, background, or style from the wrong picture. Introduce only identifiers for assets that are actually supplied and keep each identifier's meaning unchanged across all sections.

Use <Video 1> only for the user-declared motion, scene, camera, or timing role, and use <Audio 1> only for the declared voice, music, or ambience role. Do not claim to inspect or transcribe video or audio content unavailable to vision. When no reference media is supplied, write a complete prompt-driven REF2VA scene without inventing identifiers.

Return the six v5 sections in this exact order: subject_definitions, summary, retention_analysis, detailed_description, overall_soundscape, non_diegetic_music. In summary, use only these task types and join multiple applicable types with " + ": keyframe completion, reference generation, video editing, video continuation, audio reuse, audio reference. In retention_analysis, classify every supplied source independently as fully_preserved, partially_preserved, referenced, or transformed, then state the concrete details retained from it. In detailed_description, use each [Shot N] label exactly once, never timestamp Shot 1, and give later shots strictly increasing cut times.`

func IsMiniMaxH3Profile(profile Profile) bool {
	switch profile {
	case ProfileMiniMaxH3, ProfileMiniMaxH3FL2VA, ProfileMiniMaxH3REF2VA:
		return true
	default:
		return false
	}
}

func MiniMaxH3ProfileForMode(mode string) Profile {
	if mode == VideoModeREF2VA {
		return ProfileMiniMaxH3REF2VA
	}
	return ProfileMiniMaxH3FL2VA
}

func MiniMaxH3ProfileMatchesMode(profile Profile, mode string) bool {
	if mode != VideoModeFL2VA && mode != VideoModeREF2VA {
		return false
	}
	if profile == ProfileMiniMaxH3 {
		return true
	}
	return profile == MiniMaxH3ProfileForMode(mode)
}

func ValidProfile(mode Mode, profile Profile) bool {
	if mode == ModeTextToVideo {
		return IsMiniMaxH3Profile(profile)
	}
	switch profile {
	case ProfileWorkflowDefault, ProfilePhotographic, ProfileRealistic, ProfileAnime, ProfileNSFW:
		return true
	case ProfileFluxEdit, ProfileKrea2Edit:
		return mode == ModeImageToImage
	case ProfileQwen21Edit:
		return mode == ModeImageToImage
	case ProfileMiniMaxH3, ProfileMiniMaxH3FL2VA, ProfileMiniMaxH3REF2VA:
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
		return "the person or character's identity, facial features, body features and hair to transfer; not their clothing, pose, background or lighting"
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

func qwen21ReferenceMapInstruction(references []ImageReference) string {
	if len(references) == 0 {
		return ""
	}
	if len(references) == 1 {
		return "\n\nInspect the attached base image and its declared role. In the final prompt, refer to it naturally as the image; do not emit <image1> or other numbered image tags. Preserve content outside the requested edit."
	}
	ordered := append([]ImageReference(nil), references...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	lines := make([]string, 0, len(ordered))
	for _, reference := range ordered {
		if reference.Number < 1 || reference.Number > 10 || !ValidImageReferenceRole(reference.Role) {
			continue
		}
		lines = append(lines, fmt.Sprintf("- <image%d>: %s", reference.Number, referenceRoleInstruction(reference.Role)))
	}
	return "\n\nThe attached files are in the same order as these numbered slots. Inspect every supplied image and keep its role distinct. Edit <image1>; the remaining images are references only. Use the exact identifiers <image1>, <image2>, and so on for the images actually attached, including <image10> when supplied. Do not mention or invent absent images. Preserve every part of <image1> that the user did not request to change.\n" + strings.Join(lines, "\n")
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
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) {
		branchInstruction := miniMaxH3FL2VAInstruction
		if MiniMaxH3ProfileForMode(video.Mode) == ProfileMiniMaxH3REF2VA {
			branchInstruction = miniMaxH3REF2VAInstruction
		}
		instruction := miniMaxH3Instruction
		if ValidLongVideoParts(video.LongParts) {
			instruction = strings.Replace(instruction, "7000 characters", fmt.Sprintf("%d characters", PromptCharacterLimitForVideo(mode, video.LongParts)), 1)
		}
		prompt := instruction + "\n\n" + branchInstruction + "\n\n" + miniMaxH3FormatInstruction(video)
		prompt += miniMaxH3ReferenceMapInstruction(references, video)
		if video.AIHubMode != "" {
			prompt += aiHubPromptInstruction(video)
		}
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
	case ProfileKrea2Edit:
		prompt = krea2EditInstruction
	case ProfileQwen21Edit:
		prompt = qwen21EditInstruction
	}
	if mode == ModeTextToVideo && profile != ProfileWorkflowDefault {
		prompt += "\n\n" + videoInstruction
	}
	if mode == ModeImageToImage && profile != ProfileFluxEdit && profile != ProfileKrea2Edit && profile != ProfileQwen21Edit {
		prompt += workflowEditInstruction
	}
	if mode == ModeImageToImage {
		if profile == ProfileQwen21Edit {
			prompt += qwen21ReferenceMapInstruction(references)
		} else {
			prompt += referenceMapInstruction(references)
		}
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
		if reference.Number < 1 || reference.Number > 9 || !ValidImageReferenceRole(reference.Role) {
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

Inspect every attached picture before writing. In retention_analysis, identify each supplied <Picture N> separately and state only the visible source details relevant to its declared role. For identity, describe facial features, hair and other identity traits, not clothing, pose or scenery. For wardrobe, describe visible garment type, color, material and cut, not the wearer's identity. For a base scene, describe the subject, clothing, framing and setting. For a product or object, describe silhouette, material, color and construction. Use concrete visible attributes, but never invent details to meet a quota. A generic restatement such as "an adult woman in a room" or "the referenced product" is invalid. Carry only role-relevant retained attributes into subject_definitions and detailed_description. Never attribute a color, garment, object, person, pose, or background from one picture to another, and do not confuse the user's requested final state with what is visibly present in a source picture.`
}

func miniMaxH3FormatInstruction(context VideoContext) string {
	duration := context.DurationSeconds
	if context.LongParts >= 2 && context.LongParts <= 20 {
		duration = context.LongParts * 15
	} else if duration < 5 || duration > 60 {
		duration = 5
	}
	imageCount := context.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	if context.Mode == VideoModeREF2VA {
		timelineGuidance := miniMaxH3DurationGuidance(duration, "ref2va")
		if context.LongParts >= 2 && context.LongParts <= 20 {
			markers := []string{"[Shot 1]"}
			for index := 1; index < context.LongParts; index++ {
				markers = append(markers, fmt.Sprintf("[Shot %d] At %02d:%02d.000,", index+1, index*15/60, index*15%60))
			}
			timelineGuidance = fmt.Sprintf(`LONG VIDEO CONTINUATION CONTRACT: The service generates %d sequential 15-second parts, %d seconds in total. In detailed_description write exactly %d Shot sections, using these markers in order: %s. Put each marker at the beginning of a new line. These are generation windows, NOT mandatory camera cuts; any earlier instruction about cut times means part start times here. Do not add other Shot labels or restart the story at a boundary.

Plan a beginning, development and ending across the entire film, not a complete story repeated in every part. Give each part one achievable action beat with a clear opening state, gradual development and an explicit ending state that the next part continues. Do not perform a later part's action early, reset an object after it was moved, or replay completed dialogue. Keep transitions physically reachable within 15 seconds. A boundary is not a fade, freeze or stable final hold: ongoing motion and sound should cross it naturally. Place the story ending and final hold only in the final part, unless the user explicitly requests another ending.

Keep a compact shared continuity description in subject_definitions and retention_analysis: stable identity and wardrobe, handedness and held props, object locations, scene geography, lighting, camera side and movement direction, voice qualities and stable speaker IDs. Each part must carry forward the immediately preceding end state; describe any intentional change rather than silently replacing a feature. Keep screen direction and camera behavior continuous unless an intentional transition is requested. The same source references are supplied for every part; previous audio/video latent context guides the next part. Re-anchor only relevant traits instead of copying the entire global description into every part.

Use approximately 500-1100 characters per part when the request needs that detail, with concise shared sections. The complete prompt must not exceed %d characters; never pad it to the limit. Keep sound and requested music continuous, allocate exact user-supplied dialogue to achievable intervals without changing its words, and never invent dialogue. Use video continuation in summary. Return all six Context-IR sections, not a synopsis, continuation request, placeholders or an unfinished plan. Do not promise perfect identity preservation.`, context.LongParts, duration, context.LongParts, strings.Join(markers, " | "), PromptCharacterLimitForVideo(ModeTextToVideo, context.LongParts))
			if context.LongTransition == "cinematic" {
				timelineGuidance = strings.Replace(timelineGuidance, "These are generation windows, NOT mandatory camera cuts; any earlier instruction about cut times means part start times here.", "These are intentional edit points. Every part after the first starts with a clear cut on action to a new camera angle; its timestamp is the cut time.", 1)
				timelineGuidance = strings.Replace(timelineGuidance, "A boundary is not a fade, freeze or stable final hold: ongoing motion and sound should cross it naturally.", "Use a motivated hard cut, not a dissolve, morph, camera teleport within the shot, or a freeze. Carry the action across the edit without replaying it. The new shot opens in the exact physical state reached before the cut, but is already composed from the new viewpoint.", 1)
				timelineGuidance = strings.Replace(timelineGuidance, "Keep screen direction and camera behavior continuous unless an intentional transition is requested.", "Keep screen direction and the same side of the 180-degree action axis. Make each new view noticeably different: at least roughly 30 degrees of angle or a clear change in shot size. Alternate a coherent wide/medium view, three-quarter view, close-up of the active hands or object, and reaction view only when relevant; do not cut to a different place or time. Explicitly describe the new camera view at the beginning of every part after the first. Do not copy a global locked-camera restriction across this montage unless an explicit [STATIC_CAMERA] or [LOCKED_OFF] directive requires the user to choose continuous mode instead.", 1)
				timelineGuidance += "\nCINEMATIC EDITING MODE: preserve identity, wardrobe, props, completed actions and continuous audio across cuts. The renderer supplies the actual previous ending as an internal semantic continuity reference, not a fixed new camera frame. Do not invent extra reference identifiers for it in your output."
			} else {
				timelineGuidance += "\nCONTINUOUS MODE: keep the boundary camera position, framing, subject pose, grip, motion direction and speed continuous. The renderer guides from the last frames actually delivered to the viewer. Avoid rapid or occluded hand-object contact at a boundary; put the handoff during a simple, readable motion, not an arbitrary pose change."
			}
		}
		audioReference := "No standalone audio reference is attached; do not use an <Audio N> identifier."
		if context.AudioReference {
			audioReference = "One standalone <Audio 1> reference is attached and will be passed to MiniMax. When the user assigns it as a voice, music, or ambience reference, bind that exact role to <Audio 1> in the prompt. Never claim to hear, identify, or transcribe details that the user did not provide."
			if context.AIHubMode != "" && context.AudioSource == "original" {
				audioReference = "<Audio 1> is the soundtrack paired with <Video 1> in MiniMax conditioning, not a standalone voice sample. Preserve its timing. No audio listening or transcription has been performed."
			}
		}
		videoReference := "No <Video 1> reference is attached; do not use a <Video N> identifier."
		if context.VideoReference {
			videoReference = "One <Video 1> reference is attached and will be passed to MiniMax. Use the exact <Video 1> identifier when the user's request assigns it a motion, scene, camera, or timing role; never claim to inspect details that are not supplied in the user's text."
			if len(context.VideoStoryboard) > 0 {
				videoReference = "One <Video 1> is attached. Its sampled storyboard is supplied after the pictures. Ground visible scene and action in those samples, but do not invent unseen intermediate actions or claim to hear its soundtrack."
			}
		}
		retentionInstruction := "Write N/A - no reference media supplied. Do not invent <Picture N>, <Video N>, or <Audio N> identifiers."
		if imageCount > 0 || context.AudioReference || context.VideoReference {
			retentionInstruction = "State separately what every supplied <Picture N>, <Video 1>, and <Audio 1> contributes. Classify each as fully_preserved, partially_preserved, referenced, or transformed. Use only identifiers for media that is actually attached."
		}
		return fmt.Sprintf(`The selected mode is prompt-driven Ref2VA with %d attached image reference(s). Reference files are optional; this workflow remains valid with no media. Use exactly this structure:

subject_definitions:
Define every important subject and every supplied reference used by the request. When a person is present, identify the first person as <Subject 1> and ground visible attributes in the supplied pictures. Do not introduce identifiers for files that are not attached.

summary:
Use only applicable v5 task types joined with " + ": keyframe completion, reference generation, video editing, video continuation, audio reuse, audio reference. Then give one concise chronological summary.

retention_analysis:
%s %s %s

detailed_description:
Write the full %d-second chronological shot description. Start with the target visual style. Use each [Shot N] label exactly once, do not timestamp [Shot 1], and give every later shot a strictly increasing cut time.

overall_soundscape:
Describe a continuous arc of synchronized diegetic sound without repeating dialogue already present in detailed_description.

non_diegetic_music:
N/A unless requested.

%s`, imageCount, retentionInstruction, audioReference, videoReference, duration, timelineGuidance)
	}
	if imageCount >= 2 {
		return fmt.Sprintf(`The selected mode is FL2VA. Picture 1 is the exact opening frame and Picture 2 is the exact final frame. Begin exactly with:

How the reference pictures align with the target video — Picture 1 (from [Shot 1]) aligns with the 0.00-second mark of the target video; Picture 2 (from [Shot 1]) aligns with the %d.00-second mark of the target video.

Then write these fields in order:
integrated_multimodal_description: begin with exactly one [Shot 1] and describe a single continuous path that reaches Picture 2 exactly at the final timestamp. Do not add Shot 2, cuts, scene switches, or timestamp subsections. Anchor the visible scene first, distribute motion across the entire duration, mark the midpoint naturally without a timestamp, and mirror the final reference state explicitly at the end.
overall_soundscape: describe a continuous temporal arc of ambience and synchronized physical sounds without repeating dialogue.
non_diegetic_music: N/A unless requested.

If the two pictures are visually identical, motion must start immediately and return organically to the exact opening state at the final timestamp without pausing or resetting.

%s`, duration, miniMaxH3DurationGuidance(duration, "fl2va"))
	}
	if imageCount == 0 {
		return fmt.Sprintf(`The selected mode is T2VA. There is no opening or closing picture. Do not use <Picture N> identifiers. Write these fields in order:

integrated_multimodal_description: begin with [Shot 1] and describe a complete %d-second video from the user's text. Keep one coherent action and a stable final hold. Additional sequential shots are allowed only when the user's narrative needs them; use each [Shot N] once, never timestamp Shot 1, and give later shots strictly increasing cut times.
overall_soundscape: describe ambience and synchronized physical sounds without repeating dialogue.
non_diegetic_music: N/A unless requested.

%s`, duration, miniMaxH3DurationGuidance(duration, "base"))
	}
	return fmt.Sprintf(`The selected mode is I2VA. Picture 1 is the exact opening frame. Begin exactly with:

For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.

Then write these fields in order:
integrated_multimodal_description: begin with [Shot 1], reproduce the exact visible opening state, and describe a complete %d-second continuation. Additional sequential shots are allowed only when the user's narrative needs them; use each [Shot N] once, never timestamp Shot 1, and give later shots strictly increasing cut times.
overall_soundscape: describe ambience and synchronized physical sounds without repeating dialogue.
non_diegetic_music: N/A unless requested.

%s`, duration, miniMaxH3DurationGuidance(duration, "base"))
}

// AIHubTaskInstruction keeps direct generation and assistant task priorities aligned.
func AIHubTaskInstruction(task string) string {
	switch task {
	case "replace_character":
		return "Video editing: replace the character in <Video 1> using the explicitly assigned identity references. Preserve the video's action, timing, camera and environment. Change wardrobe only when a clothing reference or the user's text explicitly requests it. Do not copy static poses or backgrounds from identity photos."
	case "replace_clothing":
		return "Video editing: replace only the clothing in <Video 1> using the explicitly assigned clothing references. Preserve the original character's face, body, action, timing, camera and environment. Do not transfer the clothing model's identity or pose."
	default:
		return ""
	}
}

func aiHubPromptInstruction(video VideoContext) string {
	instruction := `

AIHUB reference-role contract:
The user's main text is the BASE_PROMPT for MiniMax H3. Produce the complete integrated English H3 prompt, not disconnected edits or an LTX prompt. Define each picture once in subject_definitions, then refer to subjects. Do not repeatedly list all pictures or repeat "locked", "exact" or "fully preserved" as a substitute for source priorities.
Respect every declared reference role. Never combine all pictures into joint facial identity. Identity pictures do not also supply wardrobe, pose, background, lighting or photographic artifacts unless requested. Body-only references do not redefine the face. When sources overlap, respect the user's priority; otherwise the first picture assigned to that role is primary and later ones support it. Do not assign an undeclared conflicting role.
For video-driven work, Video 1 drives motion and timing. Do not transfer the static poses of identity photos into the animation. Explicitly assigned photo roles may override video wardrobe or environment. For photo-only work, derive action and timing from the user's text; there is no Video 1 and pictures are not guaranteed exact first frames.
Preserve exact dialogue wording and speaker attribution, using the model's dialogue tags. Put concise non-contradictory Strict constraints inside detailed_description. Keep the six H3 sections. Text instructions are not physical locks or guarantees of artifact-free generation.
`
	instruction += "\n" + AIHubTaskInstruction(video.AIHubTask)
	switch video.AudioSource {
	case "ltx":
		if video.AudioReference {
			instruction += "\nAudio generated separately by LTX 2.5 is supplied as <Audio 1> to H3."
		} else {
			instruction += "\nAudio is generated separately by LTX 2.5, not supplied as <Audio 1> to H3."
		}
		instruction += " The user message includes a separate sound specification. Improve it in the separate audio_prompt response field for LTX, while keeping the main prompt dedicated to H3. Keep H3 dialogue/action consistent with the soundtrack. Preserve explicit dialogue verbatim in its original language; never add different dialogue or turn sound directions into spoken words."
	case "original":
		instruction += "\nThe final soundtrack is reused from Video 1. Do not invent speech, music or a transcript; no audio listening/transcription has been performed."
		if video.AudioReference {
			instruction += " Its original soundtrack is also paired with <Video 1> as <Audio 1> in H3 conditioning. Request retention of its timing and sound, without inventing unheard words."
		}
	case "none":
		instruction += "\nThe result is a still-image carousel. Do not invent audible dialogue or promise an audio track."
	default:
		instruction += "\nH3 generates the soundtrack from the main prompt. Include requested speech, ambience and music in its appropriate sections."
	}
	if video.VoiceReference {
		instruction += "\nA voice sample is connected only to Seed-VC after soundtrack generation. Preserve its declared voice-replacement purpose, but do not invent its words, gender or timbre. It is not an H3 <Audio 1> conditioning input."
	}
	if video.AIHubT8 {
		instruction += "\nT8 locks the selected external audio latent before H3 sampling. This is separate from reference conditioning and does not guarantee accurate lip synchronization."
	}
	return instruction
}

func miniMaxH3DurationGuidance(duration int, structure string) string {
	target := "1000-1800"
	soundscape := "2-3"
	transition := "6-8"
	if duration > 5 && duration <= 10 {
		target = "1500-2500"
		soundscape = "3-4"
		transition = "8-12"
	} else if duration > 10 && duration <= 15 {
		target = "2000-3500"
		soundscape = "4-5"
		transition = "12-18"
	} else if duration > 15 {
		target = "2500-5000"
		soundscape = "4-6"
		transition = "16-24"
	}
	if structure == "ref2va" && duration <= 5 {
		target = "800-1500"
	}
	if structure == "fl2va" {
		switch {
		case duration <= 5:
			target = "1200-2000"
		case duration <= 10:
			target = "2000-3000"
		case duration <= 15:
			target = "2500-4000"
		default:
			target = "3000-5500"
		}
		return fmt.Sprintf("The single [Shot 1] paragraph should contain 2-4 opening anchor sentences followed by roughly %s transition sentences. Target %s characters for the integrated description. overall_soundscape should contain %s sentences. The complete prompt must remain within 7000 characters.", transition, target, soundscape)
	}
	section := "integrated_multimodal_description"
	if structure == "ref2va" {
		section = "detailed_description"
	}
	return fmt.Sprintf("Target %s characters for %s. overall_soundscape should contain %s sentences. The complete prompt must remain within 7000 characters.", target, section, soundscape)
}
