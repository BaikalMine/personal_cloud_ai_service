package promptassistant

// EnhancementInstruction is the single instruction source for the website
// assistant and agents drafting prompts for the same workflow.
func EnhancementInstruction(mode Mode, profile Profile, references []ImageReference, video VideoContext, edit EditContext) string {
	prompt := SystemPromptWithReferences(mode, profile, references)
	if mode == ModeTextToVideo && IsMiniMaxH3Profile(profile) {
		prompt = SystemPromptWithVideoContextAndReferences(mode, profile, references, video)
	}
	prompt += structuredResponseInstruction(mode, profile, references, video)
	if mode == ModeTextToImage && edit.ImageFamily == "moss" {
		prompt += "\nThe target is MOSS-Image text-to-image, not image editing. Write a coherent English scene caption, preserving every requested subject, action, composition, medium, and relationship. Describe spatial placement explicitly when requested. Preserve all visible text verbatim in its original language, in quotes, including requested line breaks. Do not add camera jargon to an illustration, invent text, or imply an input photo exists. Do not insert sampler settings, CFG, seeds, or LoRA filenames into the image prompt. Keep the selected photographic or illustration style. Return separate positive and negative conditioning; a negative prompt lists only unwanted visual defects or explicit exclusions, not desired scene content."
	}
	if edit.CFG > 0 && profile != ProfileQwen21Edit {
		prompt += "\nAlso include a separate negative_prompt string in the JSON object. Keep it short and relevant: only unwanted visual defects or explicit exclusions, never desired scene content. Preserve useful existing exclusions and never contradict the requested style or changes. At CFG 1 return an empty negative_prompt; above CFG 1 use separate negative conditioning. Do not append negative text to the positive prompt. Maximum 4000 characters."
	}
	prompt += referenceSourcePriorityInstruction(mode, references, video)
	return prompt
}
