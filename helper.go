package backends

import (
	"encoding/json"
	"reflect"
	"strings"

	"gopkg.in/mgo.v2/bson"

	"github.com/aws/aws-sdk-go/aws/awserr"
)

// InterfaceToMap converts interface type (struct or map pointer) to *map[string]interface{}
func InterfaceToMap(object interface{}) (*map[string]interface{}, error) {
	if reflect.ValueOf(object).Kind() != reflect.Ptr {
		return nil, ErrInvalidInput("object should be of pointer type")
	}

	result := &map[string]interface{}{}
	rValue := reflect.ValueOf(object).Elem()
	rKind := rValue.Kind()

	switch rKind {

	case reflect.Struct:
		typeOfObject := rValue.Type()

		for i := 0; i < rValue.NumField(); i++ {
			f := rValue.Field(i)
			tag := typeOfObject.Field(i).Tag
			key := strings.ToLower(typeOfObject.Field(i).Name)
			if bsonName, ok := tag.Lookup("bson"); ok {
				key = bsonName
			} else if jsonName, ok := tag.Lookup("json"); ok {
				key = jsonName
			}
			if strings.Contains(key, ",") {
				key = key[0:strings.Index(key, ",")]
			}
			value := f.Interface()
			(*result)[key] = value
		}
	case reflect.Map:

		if _, ok := object.(*map[string]interface{}); ok {
			result = object.(*map[string]interface{})
		} else {
			return nil, ErrInvalidInput("invalid map type, should be *map[string]interface{}")
		}
	default:

		return nil, ErrInvalidInput("invalid object type, it should be struct pointer or *map[string]interface{}")
	}

	return result, nil
}

// MapToInterface decodes object to result
func MapToInterface(object interface{}, result interface{}) error {

	jsonStruct, err := json.Marshal(object)
	if err != nil {
		return err
	}

	json.Unmarshal(jsonStruct, result)

	return nil
}

// IterateOverSlice iterates over a slice viewed as generic itnerface{}. A callback function is called for
// every item in the slice. If the callback returns an error, the iteration will break and the function will
// return that error.
func IterateOverSlice(slice interface{}, callback func(i int, item interface{}) error) error {
	if slice == nil {
		return nil
	}

	stVal := reflect.ValueOf(slice)
	if stVal.Kind() == reflect.Ptr {
		stVal = stVal.Elem()
	}
	if stVal.Kind() != reflect.Slice {
		return ErrInvalidInput("not slice")
	}

	for i := 0; i < stVal.Len(); i++ {
		item := stVal.Index(i)
		err := callback(i, item.Interface())
		if err != nil {
			return err
		}
	}

	return nil
}

// stringToObjectID converts _id key from string to bson.ObjectId
func stringToObjectID(object map[string]interface{}) error {
	if id, ok := object["id"]; ok {
		delete(object, "id")
		if !bson.IsObjectIdHex(id.(string)) {
			return ErrInvalidInput("id is a invalid hex representation of an ObjectId")
		}

		if reflect.TypeOf(id).String() != "bson.ObjectId" {
			object["_id"] = bson.ObjectIdHex(id.(string))
		}
	}

	return nil
}

// sliceToObjectID converts _id key from slice of strings to slice of bson.ObjectId
func sliceToObjectID(object map[string]interface{}) error {
	if id, ok := object["id"]; ok {
		delete(object, "id")
		ids := strings.Split(id.(string), ",")
		bsonIds := []bson.ObjectId{}
		for _, id := range ids {
			if !bson.IsObjectIdHex(id) {
				return ErrInvalidInput("id is a invalid hex representation of an ObjectId")
			}

			if reflect.TypeOf(id).String() != "bson.ObjectId" {
				bsonIds = append(bsonIds, bson.ObjectIdHex(id))
			}
		}
		object["_id"] = bsonIds
	}

	return nil
}

// IsConditionalCheckErr check if err is dynamoDB condition error
func IsConditionalCheckErr(err error) bool {
	if ae, ok := err.(awserr.RequestFailure); ok {
		return ae.Code() == "ConditionalCheckFailedException"
	}
	return false
}

// contains checks if item is in s array
func contains(s []*string, item string) bool {
	for _, a := range s {
		if *a == item {
			return true
		}
	}
	return false
}

// CreateNewAsExample creates a new value of the same type as the "example" passed to the function.
// The function always returns a pointer to the created value.
func CreateNewAsExample(example interface{}) (interface{}, error) {
	exampleType := reflect.TypeOf(example)
	if exampleType.Kind() == reflect.Ptr {
		exampleType = exampleType.Elem()
	}

	value, err := createNewFromType(exampleType)
	if err != nil {
		return nil, err
	}
	return value.Interface(), nil
}

func createNewFromType(valueType reflect.Type) (reflect.Value, error) {
	switch kind := valueType.Kind(); kind {
	case reflect.Map:
		return valueOrError(reflect.New(valueType))
	case reflect.Slice:
		sliceVal := reflect.ValueOf(valueType)
		slen := 0
		scap := 0
		if !sliceVal.IsValid() {
			slen = sliceVal.Len()
			scap = sliceVal.Cap()
		}
		return valueOrError(reflect.MakeSlice(valueType, slen, scap))
	default:
		return valueOrError(reflect.New(valueType))
	}
}

// AsPtr returns a pointer to the value passed as an argument to this function.
// If the value is already a pointer to a value, the pointer passed is returned back
// (no new pointer is created).
func AsPtr(val interface{}) interface{} {
	valType := reflect.TypeOf(val)
	if valType.Kind() == reflect.Ptr {
		return val
	}

	return reflect.New(valType).Interface()
}

// NewSliceOfType creates new slice with len 0 and cap 0 with elements of
// the type passed as an example to the function.
func NewSliceOfType(elementTypeHint interface{}) reflect.Value {
	elemType := reflect.TypeOf(elementTypeHint)
	return reflect.MakeSlice(reflect.SliceOf(elemType), 0, 0)
}

func valueOrError(val reflect.Value) (reflect.Value, error) {
	if !val.IsValid() {
		return val, ErrInvalidInput("invalid value")
	}
	return val, nil
}
