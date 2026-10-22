// Licensed under the Apache License, Version 2.0.

//go:build driverlib

package main

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestExtensionStorageCDataRoundTrip(t *testing.T) {
	alloc := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer alloc.AssertSize(t, 0)
	typ := extensions.NewDefaultVariantType()
	builder := array.NewStructBuilder(alloc, typ.StorageType().(*arrow.StructType))
	for i := 0; i < 4; i++ {
		builder.Append(i != 2)
		builder.FieldBuilder(0).(*array.BinaryBuilder).Append([]byte{1, 0, 0})
		builder.FieldBuilder(1).(*array.BinaryBuilder).Append([]byte{0x14, byte(i), 0, 0, 0})
	}
	storage := builder.NewArray()
	builder.Release()
	variant := array.NewExtensionArrayWithStorage(typ, storage)
	storage.Release()
	defer variant.Release()
	listType := arrow.ListOf(typ)
	offsets := memory.NewBufferBytes(arrow.Int32Traits.CastToBytes([]int32{0, 1, 2, 3, 4}))
	defer offsets.Release()
	listData := array.NewData(listType, 4, []*memory.Buffer{nil, offsets}, []arrow.ArrayData{variant.Data()}, 0, 0)
	defer listData.Release()
	list := array.NewListData(listData)
	defer list.Release()
	structArray, err := array.NewStructArray([]arrow.Array{variant}, []string{"v"})
	if err != nil {
		t.Fatal(err)
	}
	defer structArray.Release()
	indicesBuilder := array.NewInt8Builder(alloc)
	indicesBuilder.AppendValues([]int8{3, 0, 2, 1}, nil)
	indices := indicesBuilder.NewArray()
	indicesBuilder.Release()
	defer indices.Release()
	dict := array.NewDictionaryArray(&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: typ}, indices, variant)
	defer dict.Release()
	cols := []arrow.Array{variant, list, structArray, dict}
	fields := make([]arrow.Field, len(cols))
	for i, col := range cols {
		fields[i] = arrow.Field{Name: string(rune('a' + i)), Type: col.DataType(), Nullable: true}
	}
	rb := array.NewRecordBatch(arrow.NewSchema(fields, nil), cols, 4)
	defer rb.Release()
	// Arrow Go's importer itself doesn't implement extension arrays yet. Import
	// the physical schema to validate every buffer; Python tests verify the
	// original extension metadata and logical schema cross the real C boundary.
	physical := arrow.NewSchema([]arrow.Field{
		{Name: "a", Type: typ.StorageType(), Nullable: true},
		{Name: "b", Type: arrow.ListOf(typ.StorageType()), Nullable: true},
		{Name: "c", Type: arrow.StructOf(arrow.Field{Name: "v", Type: typ.StorageType(), Nullable: true}), Nullable: true},
		{Name: "d", Type: &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: typ.StorageType()}, Nullable: true},
	}, nil)
	for _, bounds := range [][2]int64{{0, 4}, {1, 3}, {0, 0}} {
		part := rb.NewSlice(bounds[0], bounds[1])
		var exported cdata.CArrowArray
		exportRecordBatch(part, &exported)
		imported, err := cdata.ImportCRecordBatchWithSchema(&exported, physical)
		cdata.ReleaseCArrowArray(&exported)
		if err != nil {
			part.Release()
			t.Fatal(err)
		}
		expected := array.NewSlice(variant.(array.ExtensionArray).Storage(), bounds[0], bounds[1])
		if !array.Equal(expected, imported.Column(0)) ||
			!array.Equal(expected, imported.Column(2).(*array.Struct).Field(0)) ||
			!array.Equal(variant.(array.ExtensionArray).Storage(), imported.Column(1).(*array.List).ListValues()) ||
			!array.Equal(variant.(array.ExtensionArray).Storage(), imported.Column(3).(*array.Dictionary).Dictionary()) {
			t.Errorf("C Data round trip changed extension storage for slice %v: expected %s; top %s; struct %s; list %s; dict %s", bounds,
				expected, imported.Column(0), imported.Column(2).(*array.Struct).Field(0),
				imported.Column(1).(*array.List).ListValues(), imported.Column(3).(*array.Dictionary).Dictionary())
		}
		expected.Release()
		part.Release()
		imported.Release()
	}
}
