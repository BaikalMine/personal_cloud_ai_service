package modelmanager

import (
	"context"
	"path"
	"strings"
)

func (c *Client) RefreshMetadata(ctx context.Context, kind, file string) error {
	if !Kind(kind) || file == "" {
		return ErrInvalid
	}
	var result Result
	if err := c.JSON(ctx, "POST", "/api/lm/"+kind+"/fetch-civitai", nil, map[string]string{"file_path": file}, &result); err != nil {
		return err
	}
	if !result.Success {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) DeleteLora(ctx context.Context, file string) error {
	return c.DeleteModel(ctx, "loras", file)
}

func (c *Client) DeleteModel(ctx context.Context, kind, file string) error {
	if !Kind(kind) || file == "" || !strings.HasSuffix(strings.ToLower(file), ".safetensors") {
		return ErrInvalid
	}
	var result Result
	if err := c.JSON(ctx, "POST", "/api/lm/"+kind+"/delete", nil, map[string]string{"file_path": file}, &result); err != nil {
		return err
	}
	if !result.Success {
		return ErrUnavailable
	}
	return nil
}

// Resolve Windows paths on the Linux gateway without accepting traversal or ADS.
func LoraRelativePath(root, file string) (string, error) {
	root = strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/")
	file = strings.ReplaceAll(file, "\\", "/")
	if len(root) < 3 || root[1] != ':' || root[2] != '/' || !((root[0] >= 'A' && root[0] <= 'Z') || (root[0] >= 'a' && root[0] <= 'z')) {
		return "", ErrInvalid
	}
	if !strings.HasPrefix(strings.ToLower(file), strings.ToLower(root)+"/") {
		return "", ErrInvalid
	}
	relative := file[len(root)+1:]
	if relative == "" || path.Clean(relative) != relative || strings.ContainsAny(relative, ":\x00") {
		return "", ErrInvalid
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return "", ErrInvalid
		}
	}
	if !strings.HasSuffix(strings.ToLower(relative), ".safetensors") {
		return "", ErrInvalid
	}
	return relative, nil
}
