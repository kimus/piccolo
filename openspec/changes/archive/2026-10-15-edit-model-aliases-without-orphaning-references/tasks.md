## 1. Rename in place

- [x] 1.1 `PatchModel(doc, lookup, alias, …)` edits the found entry; collision refused before any write
- [x] 1.2 `modelRefs` walks every resolution site PicoClaw reads (verified instance.go:380-392), incl. bare-string and `{primary, fallbacks}` forms
- [x] 1.3 `RenameModelRefs` / `RemoveModel` with load-bearing refusal; fallback-only entries drop with the model

## 2. Visibility

- [x] 2.1 Doctor FAILs on an unresolved alias naming the field; WARNS on the ignored `model_name`
- [x] 2.2 Tests: rename keeps position and unknown keys, collision leaves both entries, delete refuses, absent agent file is reported not rewritten
