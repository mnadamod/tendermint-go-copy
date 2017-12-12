package db

type DB interface {

	// Get returns nil iff key doesn't exist. Panics on nil key.
	Get([]byte) []byte

	// Has checks if a key exists. Panics on nil key.
	Has(key []byte) bool

	// Set sets the key. Panics on nil key.
	Set([]byte, []byte)
	SetSync([]byte, []byte)

	// Delete deletes the key. Panics on nil key.
	Delete([]byte)
	DeleteSync([]byte)

	// Iterator over a domain of keys in ascending order. End is exclusive.
	// Start must be less than end, or the Iterator is invalid.
	// CONTRACT: No writes may happen within a domain while an iterator exists over it.
	Iterator(start, end []byte) Iterator

	// Iterator over a domain of keys in descending order. End is exclusive.
	// Start must be greater than end, or the Iterator is invalid.
	// CONTRACT: No writes may happen within a domain while an iterator exists over it.
	ReverseIterator(start, end []byte) Iterator

	// Releases the connection.
	Close()

	// Creates a batch for atomic updates.
	NewBatch() Batch

	// For debugging
	Print()

	// Stats returns a map of property values for all keys and the size of the cache.
	Stats() map[string]string
}

//----------------------------------------
// Batch

type Batch interface {
	SetDeleter
	Write()
}

type SetDeleter interface {
	Set(key, value []byte)
	Delete(key []byte)
}

//----------------------------------------

/*
	Usage:

	for itr.Seek(mykey); itr.Valid(); itr.Next() {
		k, v := itr.Key(); itr.Value()
		....
	}
*/
type Iterator interface {

	// The start & end (exclusive) limits to iterate over.
	// If end < start, then the Iterator goes in reverse order.
	//
	// A domain of ([]byte{12, 13}, []byte{12, 14}) will iterate
	// over anything with the prefix []byte{12, 13}.
	//
	// The smallest key is the empty byte array []byte{}.
	// The largest key is the nil byte array []byte(nil).
	Domain() (start []byte, end []byte)

	// Valid returns whether the current position is valid.
	// Once invalid, an Iterator is forever invalid.
	Valid() bool

	// Next moves the iterator to the next sequential key in the database, as
	// defined by the Comparator in the ReadOptions used to create this Iterator.
	//
	// If Valid returns false, this method will panic.
	Next()

	// Prev moves the iterator to the previous sequential key in the database, as
	// defined by the Comparator in the ReadOptions used to create this Iterator.
	//
	// If Valid returns false, this method will panic.
	Prev()

	// Key returns the key of the cursor.
	//
	// If Valid returns false, this method will panic.
	Key() []byte

	// Value returns the key of the cursor.
	//
	// If Valid returns false, this method will panic.
	Value() []byte

	// GetError returns an IteratorError from LevelDB if it had one during
	// iteration.
	//
	// This method is safe to call when Valid returns false.
	GetError() error

	// Close deallocates the given Iterator.
	Close()
}
