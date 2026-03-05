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
	Spouse    *TNode // actual spouse node (used in descsp mode)
	Children  []*TNode

	// layout
	X, Y float64
	Mod  float64
}

var debug bool // set via flag

// ---------------------------------------------------------------------------
// ancestor tree (child → parents)
// ---------------------------------------------------------------------------
func buildAncTree(m *Model, root string) *TNode {
	r := &TNode{
		Key:        root,
		Name:       m.Indi[root].Name,
		Birth:      m.Indi[root].Birth,
		Generation: 1,
	}
	seen := map[string]*TNode{root: r}
	stack := []*TNode{r}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		ind := m.Indi[cur.Key]
		if ind == nil || ind.ChildFam == "" {
			continue
		}
		fam := m.Fam[ind.ChildFam]
		if fam == nil {
			continue
		}

		addParent := func(ptr string) {
			if ptr == "" {
				return
			}
			if p, ok := seen[ptr]; ok { // already inserted (e.g. pedigree loop)
				cur.Children = append(cur.Children, p)
				return
			}
			pi := m.Indi[ptr]
			if pi == nil {
				return
			}
			node := &TNode{
				Key:        ptr,
				Name:       pi.Name,
				Birth:      pi.Birth,
				Generation: cur.Generation + 1,
			}
			// spouse link for parents
			if fam.Father == ptr && fam.Mother != "" {
				node.SpouseKey = fam.Mother
			}
			if fam.Mother == ptr && fam.Father != "" {
				node.SpouseKey = fam.Father
			}

			seen[ptr] = node
			cur.Children = append(cur.Children, node)
			stack = append(stack, node)
		}

		addParent(fam.Father)
		addParent(fam.Mother)
	}
	sortChildrenByBirth(r)
	return r
}

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
// descendant tree with spouse nodes (parent → children, spouses as nodes)
// ---------------------------------------------------------------------------
func buildDescSpouseTree(m *Model, root string) *TNode {
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
			continue
		}

		for i, famPtr := range ind.SpouseFams {
			fam := m.Fam[famPtr]
			if fam == nil {
				continue
			}

			// Determine spouse key
			spKey := ""
			if fam.Father == cur.Key && fam.Mother != "" {
				spKey = fam.Mother
			} else if fam.Mother == cur.Key && fam.Father != "" {
				spKey = fam.Father
			}

			// Create spouse node for first marriage
			if i == 0 && spKey != "" {
				spInd := m.Indi[spKey]
				if spInd != nil {
					cur.Spouse = &TNode{
						Key:        spKey,
						Name:       spInd.Name,
						Birth:      spInd.Birth,
						Generation: cur.Generation,
					}
				}
			}

			// Add children from this family
			for _, c := range fam.Children {
				chInd := m.Indi[normalizePtr(c)]
				if chInd == nil {
					continue
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
	}
	sortChildrenByBirth(t)
	return t
}

// ---------------------------------------------------------------------------
// Layout that accounts for spouse nodes sitting beside their partner.
// Leaves with a spouse consume 2 X slots; without, 1 slot.
// Interior nodes (with or without spouse) are centred above children.
// ---------------------------------------------------------------------------

func layoutWithSpouses(root *TNode) {
	var nextX float64
	assignXWithSpouses(root, &nextX)

	min := findMinX(root, 0)
	// also check spouse X values
	var checkSpouseMin func(n *TNode)
	checkSpouseMin = func(n *TNode) {
		if n.Spouse != nil && n.Spouse.X < min {
			min = n.Spouse.X
		}
		for _, c := range n.Children {
			checkSpouseMin(c)
		}
	}
	checkSpouseMin(root)

	if min < 0 {
		shiftWithSpouses(root, -min)
	}

	setY(root)
	// set spouse Y values
	var setSpouseY func(n *TNode)
	setSpouseY = func(n *TNode) {
		if n.Spouse != nil {
			n.Spouse.Y = n.Y
		}
		for _, c := range n.Children {
			setSpouseY(c)
		}
	}
	setSpouseY(root)
}

// coupleWidth returns how many X slots a node (with optional spouse) occupies.
// Person + connector + spouse = 3 slots when spouse present.
func coupleWidth(n *TNode) float64 {
	if n.Spouse != nil {
		return 3
	}
	return 1
}

// subtreeRight returns the rightmost X used by a node (including its spouse).
func subtreeRight(n *TNode) float64 {
	if n.Spouse != nil {
		return n.Spouse.X
	}
	return n.X
}

func assignXWithSpouses(n *TNode, next *float64) {
	if len(n.Children) == 0 {
		// Leaf node
		n.X = *next
		if n.Spouse != nil {
			n.Spouse.X = *next + 2
			*next += 3
		} else {
			*next += 1
		}
		return
	}

	// Interior node: layout children first
	for _, c := range n.Children {
		assignXWithSpouses(c, next)
	}

	// Find the full span of children (including rightmost child's spouse)
	childLeft := n.Children[0].X
	childRight := subtreeRight(n.Children[len(n.Children)-1])
	center := (childLeft + childRight) / 2

	if n.Spouse != nil {
		// Couple needs 3 slots: person at center-1, gap, spouse at center+1
		// Ensure the children span is at least 2 wide so the couple fits
		if childRight-childLeft < 2 {
			pad := 2 - (childRight - childLeft)
			center += pad / 2
			*next += pad // reserve the extra space so siblings don't overlap
		}
		n.X = center - 1
		n.Spouse.X = center + 1
	} else {
		n.X = center
	}
}

func shiftWithSpouses(n *TNode, dx float64) {
	n.X += dx
	if n.Spouse != nil {
		n.Spouse.X += dx
	}
	for _, c := range n.Children {
		shiftWithSpouses(c, dx)
	}
}

// ---------------------------------------------------------------------------
// Canvas builder for descsp mode
// ---------------------------------------------------------------------------

const connectorSize = 10 // tiny square between spouses

func BuildCanvasDescSp(doc *ged.Document, root string) *canvas.Canvas {
	model := buildModel(doc)
	tree := buildDescSpouseTree(model, root)
	layoutWithSpouses(tree)

	var cvs canvas.Canvas

	// Helper to emit a person node
	emitPerson := func(n *TNode) {
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
			ID:     n.Key,
			Type:   "text",
			X:      int(n.X * xScale),
			Y:      int(n.Y * yScale),
			Width:  nodeW,
			Height: nodeH,
			Text:   &text,
		})
	}

	// Walk tree, emit nodes + edges
	var walk func(n *TNode)
	walk = func(n *TNode) {
		emitPerson(n)

		if n.Spouse != nil {
			emitPerson(n.Spouse)

			// Connector node: small blank node centred between the couple
			connID := n.Key + "_conn"
			connX := int(((n.X + n.Spouse.X) / 2) * xScale) + nodeW/2 - connectorSize/2
			connY := int(n.Y*yScale) + nodeH/2 - connectorSize/2
			blank := ""
			cvs.Nodes = append(cvs.Nodes, &canvas.Node{
				ID:     connID,
				Type:   "text",
				X:      connX,
				Y:      connY,
				Width:  connectorSize,
				Height: connectorSize,
				Text:   &blank,
			})

			// Spouse edges: connector → person, connector → spouse
			cvs.Edges = append(cvs.Edges, &canvas.Edge{
				ID:       connID + "_to_" + n.Key,
				FromNode: connID,
				ToNode:   n.Key,
				FromSide: strptr("left"),
				ToSide:   strptr("right"),
				FromEnd:  strptr("none"),
				ToEnd:    strptr("arrow"),
			})
			cvs.Edges = append(cvs.Edges, &canvas.Edge{
				ID:       connID + "_to_" + n.Spouse.Key,
				FromNode: connID,
				ToNode:   n.Spouse.Key,
				FromSide: strptr("right"),
				ToSide:   strptr("left"),
				FromEnd:  strptr("none"),
				ToEnd:    strptr("arrow"),
			})

			// Child edges from connector
			for _, ch := range n.Children {
				cvs.Edges = append(cvs.Edges, &canvas.Edge{
					ID:       connID + "_ch_" + ch.Key,
					FromNode: connID,
					ToNode:   ch.Key,
					FromSide: strptr("bottom"),
					ToSide:   strptr("top"),
				})
			}
		} else {
			// No spouse: child edges directly from person
			for _, ch := range n.Children {
				cvs.Edges = append(cvs.Edges, &canvas.Edge{
					ID:       n.Key + "_ch_" + ch.Key,
					FromNode: n.Key,
					ToNode:   ch.Key,
					FromSide: strptr("bottom"),
					ToSide:   strptr("top"),
				})
			}
		}

		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(tree)

	return &cvs
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
	tree := buildDescTree(model, root)
	layout(tree)

	var cvs canvas.Canvas
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
	return &cvs
}

func BuildCanvasAnc(doc *ged.Document, root string) *canvas.Canvas {
	model := buildModel(doc)
	tree := buildAncTree(model, root)
	layout(tree)

	// deepest generation ⇒ used for vertical flip
	maxGen := 0.0
	var nodes []*TNode
	collect(&nodes, tree)
	for _, n := range nodes {
		if n.Y > maxGen {
			maxGen = n.Y
		}
	}

	var cvs canvas.Canvas

	// build quick lookup for node positions (by key)
	nodeByKey := make(map[string]*TNode, len(nodes))
	for _, tn := range nodes {
		nodeByKey[tn.Key] = tn
	}

	for _, n := range nodes {
		id := n.Key
		yPix := int((maxGen - n.Y) * yScale) // flip

		parts := strings.Split(n.Name, "/")
		given, surname := "", ""
		if len(parts) > 0 {
			given = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			surname = strings.TrimSpace(parts[1])
		}
		if given == "" && surname == "" {
			given = strings.TrimSpace(n.Name)
		}
		txt := fmt.Sprintf("%s\n%s", given, surname)

		cvs.Nodes = append(cvs.Nodes, &canvas.Node{
			ID:     id,
			Type:   "text",
			X:      int(n.X * xScale),
			Y:      yPix,
			Width:  nodeW,
			Height: nodeH,
			Text:   &txt,
		})

		// spouse edge (if spouse actually present in tree)
		if n.SpouseKey != "" {
			spID := normalizePtr(n.SpouseKey)

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
			})
		}
		// parent → child edges  (note: n.Children are *parents*)
		for _, p := range n.Children {
			cvs.Edges = append(cvs.Edges, &canvas.Edge{
				ID:       p.Key + "_" + id,
				FromNode: p.Key, ToNode: id,
				FromSide: strptr("bottom"), ToSide: strptr("top"),
			})
		}
	}
	return &cvs
}

func strptr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// ancsp mode: ancestors with spouses, siblings & descendants
// ---------------------------------------------------------------------------

// CrossEdge represents a connection between two trees (e.g. a marriage
// connecting two lineages).
type CrossEdge struct {
	FromConnKey string // connector node in source tree
	ToChildKey  string // child node in destination tree
}

// findAllAncestors does a BFS upward from rootID, returning every ancestor's
// ID mapped to their absolute generation number (root=0, parents=1, etc.).
func findAllAncestors(m *Model, rootID string) map[string]int {
	ancestors := map[string]int{rootID: 0}
	queue := []string{rootID}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		gen := ancestors[cur]

		ind := m.Indi[cur]
		if ind == nil || ind.ChildFam == "" {
			continue
		}
		fam := m.Fam[ind.ChildFam]
		if fam == nil {
			continue
		}
		for _, parentID := range []string{fam.Father, fam.Mother} {
			if parentID == "" {
				continue
			}
			if _, already := ancestors[parentID]; already {
				continue
			}
			if m.Indi[parentID] == nil {
				continue
			}
			ancestors[parentID] = gen + 1
			queue = append(queue, parentID)
		}
	}
	return ancestors
}

// LineageRoot identifies the oldest known ancestor in a lineage along with
// their generation number and pedigree path for ordering.
type LineageRoot struct {
	ID          string
	Generation  int
	PedigreeBit uint64 // binary path: 0=father, 1=mother at each step
	PathLen     int    // number of steps from root person to this ancestor
}

// findLineageRoots walks the ancestor map and finds people with no known
// parents in the data. They are returned in pedigree order (paternal-first).
func findLineageRoots(m *Model, rootID string, ancestors map[string]int) []LineageRoot {
	// Build pedigree paths: BFS from root, tracking the binary path.
	type entry struct {
		id      string
		bit     uint64
		pathLen int
	}

	pedigree := make(map[string]entry)
	queue := []entry{{id: rootID, bit: 0, pathLen: 0}}
	pedigree[rootID] = queue[0]

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		ind := m.Indi[cur.id]
		if ind == nil || ind.ChildFam == "" {
			continue
		}
		fam := m.Fam[ind.ChildFam]
		if fam == nil {
			continue
		}

		// Father: shift left, add 0 (father bit)
		if fam.Father != "" {
			if _, ok := ancestors[fam.Father]; ok {
				if _, seen := pedigree[fam.Father]; !seen {
					e := entry{
						id:      fam.Father,
						bit:     cur.bit << 1, // father = 0 bit
						pathLen: cur.pathLen + 1,
					}
					pedigree[fam.Father] = e
					queue = append(queue, e)
				}
			}
		}
		// Mother: shift left, add 1 (mother bit)
		if fam.Mother != "" {
			if _, ok := ancestors[fam.Mother]; ok {
				if _, seen := pedigree[fam.Mother]; !seen {
					e := entry{
						id:      fam.Mother,
						bit:     (cur.bit << 1) | 1, // mother = 1 bit
						pathLen: cur.pathLen + 1,
					}
					pedigree[fam.Mother] = e
					queue = append(queue, e)
				}
			}
		}
	}

	// Collect roots: ancestors who have no parents in the data.
	var roots []LineageRoot
	for id, gen := range ancestors {
		ind := m.Indi[id]
		if ind == nil {
			continue
		}
		isRoot := true
		if ind.ChildFam != "" {
			fam := m.Fam[ind.ChildFam]
			if fam != nil {
				if fam.Father != "" && ancestors[fam.Father] > 0 {
					isRoot = false
				}
				if fam.Mother != "" && ancestors[fam.Mother] > 0 {
					isRoot = false
				}
				// Also check if parents exist in ancestors map at all
				if _, ok := ancestors[fam.Father]; ok && fam.Father != "" {
					isRoot = false
				}
				if _, ok := ancestors[fam.Mother]; ok && fam.Mother != "" {
					isRoot = false
				}
			}
		}
		if isRoot {
			pe := pedigree[id]
			roots = append(roots, LineageRoot{
				ID:          id,
				Generation:  gen,
				PedigreeBit: pe.bit,
				PathLen:     pe.pathLen,
			})
		}
	}

	// Sort by pedigree path: shorter paths first (closer to paternal line),
	// then by bit value (father=0 before mother=1).
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].PathLen != roots[j].PathLen {
			return roots[i].PathLen < roots[j].PathLen
		}
		return roots[i].PedigreeBit < roots[j].PedigreeBit
	})

	return roots
}

// buildLineageDescTree builds a full descendant tree from a lineage root,
// going downward. It tracks which people have already been placed in a
// previous tree via the `seen` map to avoid duplicates.
func buildLineageDescTree(m *Model, rootID string, gen int, seen map[string]bool, crossEdges *[]CrossEdge) *TNode {
	rootInd := m.Indi[rootID]
	if rootInd == nil {
		return nil
	}

	root := &TNode{
		Key:        rootID,
		Name:       rootInd.Name,
		Birth:      rootInd.Birth,
		Generation: gen,
	}
	seen[rootID] = true

	type workItem struct {
		node   *TNode
		famIdx int // which spouse family to process
	}

	stack := []workItem{{node: root, famIdx: 0}}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		ind := m.Indi[cur.node.Key]
		if ind == nil || len(ind.SpouseFams) == 0 {
			continue
		}

		for i, famPtr := range ind.SpouseFams {
			fam := m.Fam[famPtr]
			if fam == nil {
				continue
			}

			// Determine spouse
			spKey := ""
			if fam.Father == cur.node.Key && fam.Mother != "" {
				spKey = fam.Mother
			} else if fam.Mother == cur.node.Key && fam.Father != "" {
				spKey = fam.Father
			}

			// Create spouse node for first marriage
			if i == 0 && spKey != "" {
				spInd := m.Indi[spKey]
				if spInd != nil {
					cur.node.Spouse = &TNode{
						Key:        spKey,
						Name:       spInd.Name,
						Birth:      spInd.Birth,
						Generation: cur.node.Generation,
					}
					seen[spKey] = true
				}
			}

			// Add children — use connector key if spouse exists, person key otherwise
			parentKey := cur.node.Key
			if cur.node.Spouse != nil {
				parentKey = cur.node.Key + "_conn"
			}
			for _, c := range fam.Children {
				cNorm := normalizePtr(c)
				if seen[cNorm] {
					// Child already placed in another tree — record cross-edge
					*crossEdges = append(*crossEdges, CrossEdge{
						FromConnKey: parentKey,
						ToChildKey:  cNorm,
					})
					continue
				}

				chInd := m.Indi[cNorm]
				if chInd == nil {
					continue
				}
				child := &TNode{
					Key:        cNorm,
					Name:       chInd.Name,
					Birth:      chInd.Birth,
					Generation: cur.node.Generation - 1,
				}
				cur.node.Children = append(cur.node.Children, child)
				seen[cNorm] = true
				stack = append(stack, workItem{node: child, famIdx: 0})
			}
			_ = i
		}
	}
	sortChildrenByBirth(root)
	return root
}

// BuildCanvasAncSp builds a canvas showing all ancestors of root with their
// siblings, spouses, and full descendant trees.
func BuildCanvasAncSp(doc *ged.Document, rootID string) *canvas.Canvas {
	model := buildModel(doc)

	// 1. Find all ancestors
	ancestors := findAllAncestors(model, rootID)

	// 2. Find lineage roots in pedigree order
	lineageRoots := findLineageRoots(model, rootID, ancestors)

	if debug {
		log.Printf("Found %d ancestors, %d lineage roots", len(ancestors), len(lineageRoots))
		for _, lr := range lineageRoots {
			ind := model.Indi[lr.ID]
			name := ""
			if ind != nil {
				name = ind.Name
			}
			log.Printf("  Lineage root: %s (%s) gen=%d pedigree=%b", lr.ID, name, lr.Generation, lr.PedigreeBit)
		}
	}

	// 3. Build descendant trees from each lineage root
	seen := make(map[string]bool)
	var crossEdges []CrossEdge
	var trees []*TNode

	for _, lr := range lineageRoots {
		if seen[lr.ID] {
			continue // already placed as a spouse in a previous tree
		}
		tree := buildLineageDescTree(model, lr.ID, lr.Generation, seen, &crossEdges)
		if tree != nil {
			trees = append(trees, tree)
		}
	}

	if debug {
		log.Printf("Built %d trees, %d cross-edges", len(trees), len(crossEdges))
	}

	// 4. Layout each tree independently, then place side by side
	var xOffset float64
	for _, tree := range trees {
		layoutWithSpouses(tree)

		// Shift tree to the right of previous trees
		if xOffset > 0 {
			shiftWithSpouses(tree, xOffset)
		}

		// Find the rightmost X in this tree (including spouses)
		maxX := findMaxXWithSpouses(tree)
		xOffset = maxX + 3 // 3-unit gap between trees (was 2, +1 for spacing)
	}

	// 5. Find maxGen for Y flip (oldest at top)
	maxGen := 0
	for _, gen := range ancestors {
		if gen > maxGen {
			maxGen = gen
		}
	}

	// 6. Emit canvas
	var cvs canvas.Canvas
	emitted := make(map[string]bool) // track emitted nodes to avoid duplicates

	emitPerson := func(n *TNode) {
		if emitted[n.Key] {
			return
		}
		emitted[n.Key] = true

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

		// Y flip: highest generation at top
		yPos := int(float64(maxGen-n.Generation) * yScale)

		cvs.Nodes = append(cvs.Nodes, &canvas.Node{
			ID:     n.Key,
			Type:   "text",
			X:      int(n.X * xScale),
			Y:      yPos,
			Width:  nodeW,
			Height: nodeH,
			Text:   &text,
		})
	}

	emittedEdges := make(map[string]bool)

	var walk func(n *TNode)
	walk = func(n *TNode) {
		emitPerson(n)

		if n.Spouse != nil {
			emitPerson(n.Spouse)

			// Connector node
			connID := n.Key + "_conn"
			if !emitted[connID] {
				emitted[connID] = true
				yPos := int(float64(maxGen-n.Generation) * yScale)
				connX := int(((n.X + n.Spouse.X) / 2) * xScale) + nodeW/2 - connectorSize/2
				connY := yPos + nodeH/2 - connectorSize/2
				blank := ""
				cvs.Nodes = append(cvs.Nodes, &canvas.Node{
					ID:     connID,
					Type:   "text",
					X:      connX,
					Y:      connY,
					Width:  connectorSize,
					Height: connectorSize,
					Text:   &blank,
				})

				// Spouse edges
				edgeID1 := connID + "_to_" + n.Key
				if !emittedEdges[edgeID1] {
					emittedEdges[edgeID1] = true
					cvs.Edges = append(cvs.Edges, &canvas.Edge{
						ID:       edgeID1,
						FromNode: connID,
						ToNode:   n.Key,
						FromSide: strptr("left"),
						ToSide:   strptr("right"),
						FromEnd:  strptr("none"),
						ToEnd:    strptr("arrow"),
					})
				}
				edgeID2 := connID + "_to_" + n.Spouse.Key
				if !emittedEdges[edgeID2] {
					emittedEdges[edgeID2] = true
					cvs.Edges = append(cvs.Edges, &canvas.Edge{
						ID:       edgeID2,
						FromNode: connID,
						ToNode:   n.Spouse.Key,
						FromSide: strptr("right"),
						ToSide:   strptr("left"),
						FromEnd:  strptr("none"),
						ToEnd:    strptr("arrow"),
					})
				}
			}

			// Child edges from connector
			connID2 := n.Key + "_conn"
			for _, ch := range n.Children {
				edgeID := connID2 + "_ch_" + ch.Key
				if !emittedEdges[edgeID] {
					emittedEdges[edgeID] = true
					cvs.Edges = append(cvs.Edges, &canvas.Edge{
						ID:       edgeID,
						FromNode: connID2,
						ToNode:   ch.Key,
						FromSide: strptr("bottom"),
						ToSide:   strptr("top"),
					})
				}
			}
		} else {
			// No spouse: child edges directly from person
			for _, ch := range n.Children {
				edgeID := n.Key + "_ch_" + ch.Key
				if !emittedEdges[edgeID] {
					emittedEdges[edgeID] = true
					cvs.Edges = append(cvs.Edges, &canvas.Edge{
						ID:       edgeID,
						FromNode: n.Key,
						ToNode:   ch.Key,
						FromSide: strptr("bottom"),
						ToSide:   strptr("top"),
					})
				}
			}
		}

		for _, ch := range n.Children {
			walk(ch)
		}
	}

	for _, tree := range trees {
		walk(tree)
	}

	// 7. Emit cross-edges
	for i, ce := range crossEdges {
		edgeID := fmt.Sprintf("cross_%d_%s_%s", i, ce.FromConnKey, ce.ToChildKey)
		cvs.Edges = append(cvs.Edges, &canvas.Edge{
			ID:       edgeID,
			FromNode: ce.FromConnKey,
			ToNode:   ce.ToChildKey,
			FromSide: strptr("bottom"),
			ToSide:   strptr("top"),
		})
	}

	return &cvs
}

// findMaxXWithSpouses returns the rightmost X value in a tree, including
// spouse nodes.
func findMaxXWithSpouses(n *TNode) float64 {
	maxX := n.X
	if n.Spouse != nil && n.Spouse.X > maxX {
		maxX = n.Spouse.X
	}
	for _, c := range n.Children {
		cx := findMaxXWithSpouses(c)
		if cx > maxX {
			maxX = cx
		}
	}
	return maxX
}

// ----------------------------------------------------------------------------
// Main (demo)
// ----------------------------------------------------------------------------

func main() {
	// Command-line flags
	gedPath := flag.String("ged", "", "Path to the GEDCOM file")
	rootPtr := flag.String("root", "", "Pointer of the root individual (e.g. @I1@)")
	mode := flag.String("mode", "desc", "Tree mode: 'desc' (descendants), 'anc' (ancestors), 'descsp' (descendants with spouses), 'ancsp' (ancestors with spouses/siblings/descendants)")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")

	flag.Parse()

	// Basic validation
	if *gedPath == "" || *rootPtr == "" {
		fmt.Println("usage: gedcanvas -ged <gedcom-file> -root <root-pointer> [-mode desc]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *mode != "desc" && *mode != "anc" && *mode != "descsp" && *mode != "ancsp" {
		log.Fatalf("unsupported mode %q (supported: 'desc', 'anc', 'descsp', 'ancsp')", *mode)
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
	case "anc":
		cvs = BuildCanvasAnc(doc, resolvedPtr)
	case "descsp":
		cvs = BuildCanvasDescSp(doc, resolvedPtr)
	case "ancsp":
		cvs = BuildCanvasAncSp(doc, resolvedPtr)
	default:
		log.Fatalf("unsupported mode %q (supported: 'desc', 'anc', 'descsp')", *mode)
	}

	data, _ := json.MarshalIndent(cvs, "", "  ")
	out := strings.TrimSuffix(*gedPath, ".ged") + ".canvas"
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
