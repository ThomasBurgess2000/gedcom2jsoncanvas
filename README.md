# gedcom2jsoncanvas

Generates JSONCanvas files (compatible with [Obsidian](https://obsidian.md)) from GEDCOM (.ged) ancestry files. Supports multiple tree modes for visualizing family relationships.

Here is an example of the ancestor mapping:

<img width="2501" height="1539" alt="image" src="https://github.com/user-attachments/assets/7d3a27e9-c3de-43db-b42f-bc82afe312f9" />

## Modes

| Mode | Flag | Description |
|------|------|-------------|
| `desc` | `-mode desc` | Descendant tree from root person downward |
| `anc` | `-mode anc` | Ancestor tree from root person upward |
| `descsp` | `-mode descsp` | Descendant tree with spouse nodes and connector edges |
| `ancsp` | `-mode ancsp` | Full ancestor tree with spouses, siblings, and their descendant subtrees. Builds a forest of descendant trees from each oldest-known ancestor, laid out side by side with cross-marriage edges connecting lineages. |

## Getting Started

```
go run main.go -ged <gedcom-file> -root <root-pointer> -mode <mode>
```

Example using the test file:

```
go run main.go -ged test.ged -root I54 -mode anc
```

The output `.canvas` file will be written alongside the input `.ged` file with the same base name. Open it in Obsidian to view.

### Flags

| Flag | Description |
|------|-------------|
| `-ged` | Path to the GEDCOM file |
| `-root` | Pointer of the root individual (e.g. `I54`, `@I1@`, or a name search like `"John Smith"`) |
| `-mode` | Tree mode (default: `desc`) |
| `-debug` | Enable debug logging |
