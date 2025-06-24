package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	gedcom "github.com/elliotchance/gedcom"
	"github.com/supersonicpineapple/go-jsoncanvas/canvas"
)

// simple constants for node sizing
const (
	nodeWidth  = 200
	nodeHeight = 100
)

// buildCanvas converts a gedcom document to a JSON Canvas structure.
// Currently it creates one text node per individual and does not add edges.
func buildCanvas(doc *gedcom.Document) *canvas.Canvas {
	c := &canvas.Canvas{}

	individuals := doc.Individuals()

	// Helper to strip surrounding "@" from GEDCOM pointers.
	sanitize := func(ptr string) string {
		if len(ptr) > 1 && ptr[0] == '@' && ptr[len(ptr)-1] == '@' {
			return ptr[1 : len(ptr)-1]
		}
		return ptr
	}

	// Keep a map so we can quickly look up whether we have already created a node for a pointer.
	nodeIndex := make(map[string]struct{})

	for idx, ind := range individuals {
		if ind == nil {
			continue
		}

		// Generate a simple textual representation for the node.
		label := ind.Name().String()

		nodeID := sanitize(ind.Pointer())

		n := &canvas.Node{
			ID:     nodeID,
			Type:   "text",
			Text:   &label,
			X:      idx % 5 * (nodeWidth + 50), // 5 columns grid
			Y:      (idx / 5) * (nodeHeight + 50),
			Width:  nodeWidth,
			Height: nodeHeight,
		}

		c.Nodes = append(c.Nodes, n)
		nodeIndex[nodeID] = struct{}{}
	}

	// ------------------------------------------------------------------
	// Build edges.
	// 1. Unidirectional edges from parents to children.
	// 2. Bidirectional edges between spouses (two opposite edges).

	edgeSeen := make(map[string]struct{})
	arrow := "arrow"

	addEdge := func(fromID, toID string, fromSide, toSide string) {
		key := fromID + "->" + toID
		if _, exists := edgeSeen[key]; exists {
			return
		}
		edgeSeen[key] = struct{}{}

		// Only create the edge if both nodes exist (they should, but be safe).
		if _, ok := nodeIndex[fromID]; !ok {
			return
		}
		if _, ok := nodeIndex[toID]; !ok {
			return
		}

		// Copy side strings to avoid taking address of loop var.
		fs, ts := fromSide, toSide

		e := &canvas.Edge{
			ID:       fmt.Sprintf("%d", len(c.Edges)+1),
			FromNode: fromID,
			FromSide: &fs,
			ToNode:   toID,
			ToSide:   &ts,
			ToEnd:    &arrow,
		}
		c.Edges = append(c.Edges, e)
	}

	// Iterate over families to establish relationships.
	for _, fam := range doc.Families() {
		if fam == nil {
			continue
		}

		var parents []*gedcom.IndividualNode

		if h := fam.Husband(); h != nil && h.Individual() != nil {
			parents = append(parents, h.Individual())
		}
		if w := fam.Wife(); w != nil && w.Individual() != nil {
			parents = append(parents, w.Individual())
		}

		// Parent -> child edges.
		for _, childNode := range fam.Children() {
			childInd := childNode.Individual()
			if childInd == nil {
				continue
			}
			childID := sanitize(childInd.Pointer())

			for _, p := range parents {
				if p == nil {
					continue
				}
				parentID := sanitize(p.Pointer())
				addEdge(parentID, childID, "bottom", "top")
			}
		}

		// Spouse bidirectional edges.
		if len(parents) == 2 {
			idA := sanitize(parents[0].Pointer())
			idB := sanitize(parents[1].Pointer())

			addEdge(idA, idB, "right", "left")
			addEdge(idB, idA, "left", "right")
		}
	}

	return c
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <file.ged> [output.canvas]", os.Args[0])
	}

	inputPath := os.Args[1]
	outputPath := "output.canvas"
	if len(os.Args) >= 3 {
		outputPath = os.Args[2]
	}

	f, err := os.Open(inputPath)
	if err != nil {
		log.Fatalf("failed to open GEDCOM file: %v", err)
	}
	defer f.Close()

	decoder := gedcom.NewDecoder(f)
	doc, err := decoder.Decode()
	if err != nil {
		log.Fatalf("failed to parse GEDCOM: %v", err)
	}

	jc := buildCanvas(doc)

	out, err := os.Create(outputPath)
	if err != nil {
		log.Fatalf("failed to create output file: %v", err)
	}
	defer out.Close()

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(jc); err != nil {
		log.Fatalf("failed to write JSON Canvas: %v", err)
	}

	fmt.Printf("Successfully wrote %d nodes and %d edges to %s\n", len(jc.Nodes), len(jc.Edges), outputPath)
}
