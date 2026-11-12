package domain

import "testing"

func TestPostprocessFamilyPermissions(t *testing.T) {
	for _, tc := range []struct {
		user         User
		image, video bool
	}{
		{User{}, false, false},
		{User{CanGenerateTextToImage: true, CanGenerateVideo: true}, false, false},
		{User{CanUseQuickGeneration: true}, false, false},
		{User{CanUseQuickGeneration: true, CanGenerateTextToImage: true}, true, false},
		{User{CanUseQuickGeneration: true, CanGenerateImageToImage: true}, true, false},
		{User{CanUseQuickGeneration: true, CanGenerateVideo: true}, false, true},
		{User{Role: "admin"}, true, true},
	} {
		if tc.user.CanUseQuickGenerationType(PostprocessImageTemplate) != tc.image || tc.user.CanUseQuickGenerationType(PostprocessVideoTemplate) != tc.video {
			t.Fatalf("wrong postprocess permissions: %+v", tc)
		}
	}
}
