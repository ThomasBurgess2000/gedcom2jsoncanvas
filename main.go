package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"flag"

	ged "github.com/elliotchance/gedcom"
	"github.com/supersonicpineapple/go-jsoncanvas/canvas"
)

// ----------------------------------------------------------------------------
// GEDCOM flattening
// ----------------------------------------------------------------------------

type Individual struct {
	ID         string
	Name       string
	Birth      string
	Sex        string
	ChildFam   string   // FAMC
	SpouseFams []string // many FAMS tags
}

type Family struct {
	ID       string
	Father   string
	Mother   string
	Children []string
}

type Model struct {
	Indi map[string]*Individual
	Fam  map[string]*Family
}

// ---------------------------------------------------------------------------
// helpers – work with the current gedcom API
// ---------------------------------------------------------------------------

// spouseFamilyPointers returns every family pointer where ind is a spouse.
func spouseFamilyPointers(ind *ged.IndividualNode) []string {
	var famPtrs []string
	for _, fam := range ind.Families() {
		if fam.HasChild(ind) {
			// skip child families; we only want families where the person is spouse
			continue
		}
		famPtrs = append(famPtrs, fam.Pointer())
	}
	return famPtrs
}

// childFamilyPointer returns the family pointer where ind is listed as a child,
// or "" if none exists. Pointer comparisons are normalised so that different
// capitalisation or surrounding "@" delimiters do not break the lookup.
func childFamilyPointer(ind *ged.IndividualNode, childToFam map[string]string) string {
	return childToFam[normalizePtr(ind.Pointer())]
}

// ---------------------------------------------------------------------------
// model-builder that works with the new API
// ---------------------------------------------------------------------------

func buildModel(doc *ged.Document) *Model {
	m := &Model{
		Indi: make(map[string]*Individual),
		Fam:  make(map[string]*Family),
	}

	// ---------- first pass: index every family ----------
	childToFam := make(map[string]string) // @I@ → @F@ (for quick lookup)

	for _, f := range doc.Families() {
		fam := &Family{ID: f.Pointer()}

		if h := f.Husband(); h != nil {
			fam.Father = normalizePtr(h.Value())
		}
		if w := f.Wife(); w != nil {
			fam.Mother = normalizePtr(w.Value())
		}
		for _, c := range f.Children() {
			ptr := normalizePtr(c.Value())
			fam.Children = append(fam.Children, ptr)
			// Store by normalised key for consistent, reliable look-ups.
			childToFam[ptr] = f.Pointer() // remember: this child belongs to fam
		}
		m.Fam[f.Pointer()] = fam
	}

	// ---------- second pass: build every individual ----------
	for _, n := range doc.Individuals() {
		ptr := normalizePtr(n.Pointer())
		ind := &Individual{
			ID:   ptr,
			Name: n.Name().String(),
			Sex:  n.Sex().String(),
		}
		if b, _ := n.Birth(); b != nil {
			ind.Birth = b.String()
		}

		ind.SpouseFams = spouseFamilyPointers(n)
		ind.ChildFam = childFamilyPointer(n, childToFam)

		m.Indi[ptr] = ind
	}

	return m
}

// ----------------------------------------------------------------------------
// Tree + RT layout
// ----------------------------------------------------------------------------

type TNode struct {
	Key        string
	Name       string
	Birth      string
	Generation int

	SpouseKey string
	Children  []*TNode

	// layout
	X, Y float64
	Mod  float64
}

var debug bool // set via flag

// ---------------------------------------------------------------------------
// descendant tree (parent → children)
// ---------------------------------------------------------------------------
func buildDescTree(m *Model, root string) *TNode {
	rootInd, ok := m.Indi[root]
	if !ok || rootInd == nil {
		log.Fatalf("root individual %q not found in GEDCOM", root)
	}

	t := &TNode{
		Key:        root,
		Name:       rootInd.Name,
		Birth:      rootInd.Birth,
		Generation: 1,
	}
	stack := []*TNode{t}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ind := m.Indi[cur.Key]
		if ind == nil || len(ind.SpouseFams) == 0 {
			if debug {
				log.Printf("no spouse families for %s", cur.Key)
			}
			continue
		}
		famPtr := ind.SpouseFams[0]
		fam := m.Fam[famPtr]
		if fam == nil {
			log.Printf("warning: family %q referenced by %q not found", famPtr, ind.ID)
			continue
		}
		if debug {
			log.Printf("processing family %s for %s; children: %d", famPtr, cur.Key, len(fam.Children))
		}

		// spouse
		if fam.Father == cur.Key && fam.Mother != "" {
			cur.SpouseKey = fam.Mother
		} else if fam.Mother == cur.Key && fam.Father != "" {
			cur.SpouseKey = fam.Father
		}

		// children
		for _, c := range fam.Children {
			chInd := m.Indi[normalizePtr(c)]
			if chInd == nil {
				log.Printf("warning: child %q referenced in family %q not found", c, famPtr)
				continue
			}
			if debug {
				log.Printf("adding child %s to %s", c, cur.Key)
			}
			child := &TNode{
				Key:        c,
				Name:       chInd.Name,
				Birth:      chInd.Birth,
				Generation: cur.Generation + 1,
			}
			cur.Children = append(cur.Children, child)
			stack = append(stack, child)
		}
	}
	sortChildrenByBirth(t)
	return t
}

func sortChildrenByBirth(n *TNode) {
	if len(n.Children) > 1 {
		sort.Slice(n.Children, func(i, j int) bool {
			return n.Children[i].Birth < n.Children[j].Birth
		})
	}
	for _, c := range n.Children {
		sortChildrenByBirth(c)
	}
}

// ---------------------------------------------------------------------------
// Simple tidy-ish layout: depth-first, in-order traversal assigns successive X
// positions to leaf nodes ensuring every node within the same generation gets
// its own column. Interior nodes are centred above their children. This avoids
// overlapping without the complexity of a full Reingold-Tilford algorithm.
// ---------------------------------------------------------------------------

func layout(root *TNode) {
	var nextX float64
	assignX(root, &nextX)

	// normalise so that minimum X is zero (cosmetic)
	min := findMinX(root, 0)
	if min < 0 {
		shift(root, -min)
	}

	// populate Y using generation (1-based) so that vertical spacing is
	// consistent between ancestor and descendant layouts.
	setY(root)
}

// assignX recursively assigns horizontal positions.
func assignX(n *TNode, next *float64) {
	if len(n.Children) == 0 {
		n.X = *next
		*next += 1
		return
	}
	for _, c := range n.Children {
		assignX(c, next)
	}
	n.X = (n.Children[0].X + n.Children[len(n.Children)-1].X) / 2
}

func findMinX(n *TNode, currentMin float64) float64 {
	if n.X < currentMin {
		currentMin = n.X
	}
	for _, c := range n.Children {
		currentMin = findMinX(c, currentMin)
	}
	return currentMin
}

func shift(n *TNode, dx float64) {
	n.X += dx
	for _, c := range n.Children {
		shift(c, dx)
	}
}

// helper
func collect(nodes *[]*TNode, n *TNode) {
	*nodes = append(*nodes, n)
	for _, c := range n.Children {
		collect(nodes, c)
	}
}

func setY(n *TNode) {
	n.Y = float64(n.Generation - 1)
	for _, c := range n.Children {
		setY(c)
	}
}

// ----------------------------------------------------------------------------
// Canvas generation
// ----------------------------------------------------------------------------

const (
	nodeW   = 200
	nodeH   = 100
	xScale  = 300 // horizontal gap
	yScale  = 180 // vertical gap
	edgePad = 15
)

func BuildCanvas(doc *ged.Document, root string) *canvas.Canvas {
	model := buildModel(doc)
	var cvs canvas.Canvas

	// build tree for every individual
	for _, ind := range model.Indi {
		tree := buildDescTree(model, ind.ID)

		layout(tree)

		var nodes []*TNode
		collect(&nodes, tree)

		// build quick lookup for node positions (by key)
		nodeByKey := make(map[string]*TNode, len(nodes))
		for _, tn := range nodes {
			nodeByKey[tn.Key] = tn
		}

		for _, n := range nodes {
			id := n.Key
			// GEDCOM NAME field is typically "Given /Surname/" but may vary.
			parts := strings.Split(n.Name, "/")
			var given, surname string
			if len(parts) > 0 {
				given = strings.TrimSpace(parts[0])
			}
			if len(parts) > 1 {
				surname = strings.TrimSpace(parts[1])
			}
			if given == "" && surname == "" {
				given = strings.TrimSpace(n.Name)
			}
			text := fmt.Sprintf("%s\n%s", given, surname)
			cvs.Nodes = append(cvs.Nodes, &canvas.Node{
				ID:     id,
				Type:   "text",
				X:      int(n.X * xScale),
				Y:      int(n.Y * yScale),
				Width:  nodeW,
				Height: nodeH,
				Text:   &text,
			})
			if n.SpouseKey != "" {
				spID := normalizePtr(n.SpouseKey)
				label := "spouse"

				// Decide edge sides based on horizontal positions.
				fromSide, toSide := "right", "left" // defaults (n on left of spouse)
				if spNode, ok := nodeByKey[spID]; ok {
					if n.X > spNode.X { // n is to the right of spouse
						fromSide, toSide = "left", "right"
					}
				}

				cvs.Edges = append(cvs.Edges, &canvas.Edge{
					ID:       id + "_" + spID,
					FromNode: id,
					ToNode:   spID,
					FromSide: strptr(fromSide),
					ToSide:   strptr(toSide),
					Label:    &label,
				})
			}
			for _, ch := range n.Children {
				cvs.Edges = append(cvs.Edges, &canvas.Edge{
					ID:       id + "_" + ch.Key,
					FromNode: id,
					ToNode:   ch.Key,
					FromSide: strptr("bottom"),
					ToSide:   strptr("top"),
				})
			}
		}
	}
	return &cvs
}

func strptr(s string) *string { return &s }

// ----------------------------------------------------------------------------
// Main (demo)
// ----------------------------------------------------------------------------

func main() {
	// Command-line flags
	gedPath := flag.String("ged", "", "Path to the GEDCOM file")
	rootPtr := flag.String("root", "", "Pointer of the root individual (e.g. @I1@)")
	mode := flag.String("mode", "desc", "Tree mode: currently only 'desc' (descendants) is supported")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")

	flag.Parse()

	// Basic validation
	if *gedPath == "" || *rootPtr == "" {
		fmt.Println("usage: gedcanvas -ged <gedcom-file> -root <root-pointer> [-mode desc]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *mode != "desc" {
		log.Fatalf("unsupported mode %q (only 'desc' is implemented)", *mode)
	}

	raw, err := os.ReadFile(*gedPath)
	if err != nil {
		log.Fatal(err)
	}
	doc, err := ged.NewDecoder(strings.NewReader(string(raw))).Decode()
	if err != nil {
		log.Fatal(err)
	}

	resolvedPtr, err := resolveRootPointer(doc, *rootPtr)
	if err != nil {
		log.Fatalf("cannot resolve root: %v", err)
	}

	var cvs *canvas.Canvas
	switch *mode {
	case "desc":
		cvs = BuildCanvas(doc, resolvedPtr)
	default:
		log.Fatalf("unsupported mode %q (only 'desc' is implemented)", *mode)
	}

	data, _ := json.MarshalIndent(cvs, "", "  ")
	out := "C:\\Users\\thoma\\Desktop\\Family Tree\\familytree.canvas"
	// out := strings.TrimSuffix(*gedPath, ".ged") + ".canvas"
	_ = os.WriteFile(out, data, 0644)
	fmt.Println("wrote", out)
}

// normalizePtr strips "@" delimiters and converts to upper-case so pointers can
// be compared in a case-insensitive, delimiter-agnostic way.
func normalizePtr(p string) string {
	return strings.ToUpper(strings.Trim(strings.TrimSpace(p), "@"))
}

func resolveRootPointer(doc *ged.Document, input string) (string, error) {
	trimmed := strings.TrimSpace(input)

	// Try to match on pointer first, using relaxed comparison rules.
	inNorm := normalizePtr(trimmed)
	for _, ind := range doc.Individuals() {
		if normalizePtr(ind.Pointer()) == inNorm {
			return ind.Pointer(), nil // return canonical pointer from file
		}
	}

	// Otherwise, treat the input as a case-insensitive name query.
	var matches []string
	q := strings.ToLower(trimmed)
	for _, ind := range doc.Individuals() {
		fullName := strings.ToLower(ind.Name().String())
		if strings.Contains(fullName, q) {
			matches = append(matches, ind.Pointer())
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no individual matching %q found", input)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous root query %q: matches %v", input, matches)
	}
}
