package domain

// OutputRelation is the side of another output where an output is placed.
type OutputRelation uint8

const (
	RelationNone OutputRelation = iota
	RelationRightOf
	RelationLeftOf
	RelationAbove
	RelationBelow
)
