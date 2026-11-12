# Task Media Fixture

`task-preview.mp4` is a one-second CPU-encoded H.264 fixture derived from the
repository's existing `internal/promptassistant/testdata/visual-v1/images/03.jpg`.
It is used only by the read-only UI preview. It is not a generated user result.
The task-list poster is produced through `internal/thumbnail`, not substituted
with the original JPEG, and no source video is loaded by a list row.
