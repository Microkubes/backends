package backends

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/Microkubes/microservice-tools/config"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

// MONGO_CTX_KEY is mongoDB context key
var MONGO_CTX_KEY = "MONGO_SESSION"

// MongoSession wraps a mgo.Session to embed methods in models.
type MongoSession struct {
	Session        *mgo.Session
	repoDef        RepositoryDefinition
	databaseName   string
	collectionName string
}

// GetCollection returns the collection and a session to be closed after
func (s *MongoSession) GetCollection() (*mgo.Session, *mgo.Collection) {
	session := s.Session.Copy()
	c := session.DB(s.databaseName).C(s.collectionName)
	return session, c
}

// MongoDBRepoBuilder builds new mongo collection.
// If it does not exist builder will create it
func MongoDBRepoBuilder(repoDef RepositoryDefinition, backend Backend) (Repository, error) {

	sessionObj := backend.GetFromContext(MONGO_CTX_KEY)
	if sessionObj == nil {
		return nil, ErrBackendError("mongo session not configured")
	}

	session, ok := sessionObj.(*mgo.Session)
	if !ok {
		return nil, ErrBackendError("unknown session type")
	}

	databaseName := backend.GetConfig().DatabaseName
	if databaseName == "" {
		return nil, ErrBackendError("database name is missing and required")
	}

	collectionName := repoDef.GetName()
	if collectionName == "" {
		return nil, ErrBackendError("collection name is missing and required")
	}

	_, err := PrepareDB(
		session,
		databaseName,
		collectionName,
		repoDef.GetIndexes(),
		repoDef.EnableTTL(),
		repoDef.GetTTL(),
		repoDef.GetTTLAttribute(),
	)

	if err != nil {
		return nil, err
	}

	return &MongoSession{
		Session:        session,
		repoDef:        repoDef,
		databaseName:   databaseName,
		collectionName: collectionName,
	}, nil
}

// MongoDBBackendBuilder returns RepositoriesBackend
func MongoDBBackendBuilder(conf *config.DBInfo, manager BackendManager) (Backend, error) {

	session, err := NewSession(conf.Host, conf.Username, conf.Password, conf.DatabaseName)
	if err != nil {
		return nil, err
	}

	ctx := context.WithValue(context.Background(), MONGO_CTX_KEY, session)
	cleanup := func() {
		session.Close()
	}

	return NewRepositoriesBackend(ctx, conf, MongoDBRepoBuilder, cleanup), nil
}

// NewSession returns a new Mongo Session.
func NewSession(Host string, Username string, Password string, Database string) (*mgo.Session, error) {

	session, err := mgo.DialWithInfo(&mgo.DialInfo{
		Addrs:    []string{Host},
		Username: Username,
		Password: Password,
		Database: Database,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	// SetMode - consistency mode for the session.
	session.SetMode(mgo.Monotonic, true)

	return session, nil
}

// PrepareDB ensure presence of persistent and immutable data in the DB. It creates indexes
func PrepareDB(session *mgo.Session, db string, dbCollection string, indexes []Index, enableTTL bool, TTL int, TTLField string) (*mgo.Collection, error) {

	collection := session.DB(db).C(dbCollection)

	// Define indexes
	for _, elem := range indexes {
		i := elem.GetFields()
		index := mgo.Index{
			Key:        i,
			Unique:     elem.Unique(),
			DropDups:   true,
			Background: true,
			Sparse:     true,
		}

		// Create indexes
		if err := collection.EnsureIndex(index); err != nil {
			if qe, ok := err.(*mgo.QueryError); ok {
				if qe.Code == 85 {
					// IndexOptionsConflict - see here https://github.com/mongodb/mongo/blob/master/src/mongo/base/error_codes.err
					// It means that there is already defined index and we try to redefine it, which is (mostly) fine.
					log.Println("WARN: The index already exists and will not be updated. MongoDB error: ", err.Error())
				}
			} else {
				log.Println("ERROR: while creating index. of type: ", reflect.TypeOf(err), " and values: ", fmt.Sprintf("%v", err))
				return nil, err
			}
		}
	}

	if enableTTL == true {
		if TTLField == "" {
			return nil, ErrBackendError("TTL attribute is reqired when TTL is enabled")
		}

		if TTL == 0 {
			return nil, ErrBackendError("TTL value is missing and must be greater than zero")
		}

		index := mgo.Index{
			Key:         []string{TTLField},
			Unique:      false,
			DropDups:    false,
			Background:  true,
			Sparse:      true,
			ExpireAfter: time.Duration(TTL) * time.Second,
		}
		if err := collection.EnsureIndex(index); err != nil {
			return nil, err
		}

	}

	return collection, nil
}

// GetOne fetches only one record for given filter
func (s *MongoSession) GetOne(filter Filter, result interface{}) (interface{}, error) {
	session, c := s.GetCollection()
	defer session.Close()

	var record map[string]interface{}

	if !s.repoDef.IsCustomID() {
		if err := stringToObjectID(filter); err != nil {
			return nil, err
		}
	}

	err := c.Find(filter).One(&record)
	if err != nil {
		if err == mgo.ErrNotFound {
			return nil, err
		}
		return nil, err
	}
	if s.repoDef.IsCustomID() {
		record["_id"] = record["_id"].(bson.ObjectId).Hex()
	} else {
		record["id"] = record["_id"].(bson.ObjectId).Hex()
	}

	err = MapToInterface(&record, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// GetAll fetches all matched records for given filter
func (s *MongoSession) GetAll(filter Filter, resultsTypeHint interface{}, order string, sorting string, limit int, offset int) (interface{}, error) {
	session, c := s.GetCollection()
	defer session.Close()

	resultsTypeHint = AsPtr(resultsTypeHint)
	results := NewSliceOfType(resultsTypeHint)

	// Create a pointer to a slice value and set it to the slice
	slicePointer := reflect.New(results.Type())
	slicePointer.Elem().Set(results)

	if !s.repoDef.IsCustomID() {
		if id, ok := filter["id"]; ok {
			// check if id field contains values separated by comma
			if ok := strings.Contains(id.(string), ","); ok {
				if err := sliceToObjectID(filter); err != nil {
					return nil, ErrInvalidInput(err)
				}
			} else {
				if err := stringToObjectID(filter); err != nil {
					return nil, ErrInvalidInput(err)
				}
			}
		}
	}

	mongoFilter, err := toMongoFilter(filter)
	if err != nil {
		return nil, ErrInvalidInput(err)
	}

	query := c.Find(mongoFilter)
	if order != "" {
		if sorting == "desc" {
			order = "-" + order
		}
		query = query.Sort(order)
	}
	if offset != 0 {
		query = query.Skip(offset)
	}
	if limit != 0 {
		query = query.Limit(limit)
	}

	err = query.All(slicePointer.Interface())
	if err != nil {
		if err == mgo.ErrNotFound {
			return nil, ErrNotFound(err)
		}
		return nil, err
	}

	// results is always a Slice
	err = IterateOverSlice(slicePointer.Interface(), func(i int, item interface{}) error {
		if item == nil {
			return nil // ignore
		}

		itemValue := reflect.ValueOf(item)
		itemType := reflect.TypeOf(item)
		if itemType.Kind() == reflect.Ptr {
			// item is pointer to something
			itemType = itemType.Elem()
			itemValue = reflect.Indirect(itemValue)
		}

		if itemType.Kind() == reflect.Map {
			// we have a map[string]<some-type>
			idValue := itemValue.MapIndex(reflect.ValueOf("_id"))
			if idValue.IsValid() {
				// ok,there is such value
				if bsonID, ok := idValue.Interface().(bson.ObjectId); ok {
					idStr := bsonID.Hex()
					if s.repoDef.IsCustomID() {
						// we have a custom handling on property "id", so we'll map _id => HEX(_id)
						itemValue.SetMapIndex(reflect.ValueOf("_id"), reflect.ValueOf(idStr))
					} else {
						// no custom mapping set, so the default behaviour is to map id => HEX(_id)
						itemValue.SetMapIndex(reflect.ValueOf("id"), reflect.ValueOf(idStr))
						itemValue.SetMapIndex(reflect.ValueOf("_id"), reflect.Value{})
					}

				}
			}
		}

		return nil
	})

	return slicePointer.Interface(), nil
}

// Save creates new record unless it does not exist, otherwise it updates the record
func (s *MongoSession) Save(object interface{}, filter Filter) (interface{}, error) {
	session, c := s.GetCollection()
	defer session.Close()

	var result interface{}

	payload, err := InterfaceToMap(object)
	if err != nil {
		return nil, err
	}

	if filter == nil {

		id := bson.NewObjectId()
		(*payload)["_id"] = id
		if !s.repoDef.IsCustomID() {
			delete(*payload, "id")
		}

		err = c.Insert(payload)
		if err != nil {
			if mgo.IsDup(err) {
				return nil, ErrAlreadyExists("record already exists!")
			}
			return nil, err
		}

		if !s.repoDef.IsCustomID() {
			(*payload)["id"] = id.Hex()
		}
		err = MapToInterface(payload, &object)
		if err != nil {
			return nil, err
		}

		return object, nil
	}

	if !s.repoDef.IsCustomID() {
		if err := stringToObjectID(filter); err != nil {
			return nil, ErrInvalidInput(err)
		}
	}

	if _, ok := (*payload)["_id"]; ok {
		// we can't update MongoDB's own id - it is immutable.
		delete(*payload, "_id")
	}

	err = c.Update(filter, bson.M{"$set": payload})
	if err != nil {
		if err == mgo.ErrNotFound {
			return nil, ErrNotFound(err)
		}
		if mgo.IsDup(err) {
			return nil, ErrAlreadyExists("record already exists!")
		}

		return nil, err
	}

	result, err = s.GetOne(filter, object)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// DeleteOne deletes only one record for given filter
func (s *MongoSession) DeleteOne(filter Filter) error {
	session, c := s.GetCollection()
	defer session.Close()

	if !s.repoDef.IsCustomID() {
		if err := stringToObjectID(filter); err != nil {
			return ErrInvalidInput(err)
		}
	}

	err := c.Remove(filter)
	if err != nil {
		if err == mgo.ErrNotFound {
			return ErrNotFound(err)
		}
		return err
	}

	return nil
}

// DeleteAll deletes all matched records for given filter
func (s *MongoSession) DeleteAll(filter Filter) error {
	session, c := s.GetCollection()
	defer session.Close()

	if !s.repoDef.IsCustomID() {
		if err := stringToObjectID(filter); err != nil {
			return ErrInvalidInput(err)
		}
	}

	_, err := c.RemoveAll(filter)
	if err != nil {
		if err == mgo.ErrNotFound {
			return ErrNotFound(err)
		}
		return err
	}

	return nil
}

func toMongoFilter(filter Filter) (map[string]interface{}, error) {
	mgf := map[string]interface{}{}
	for key, value := range filter {
		if specs, ok := value.(map[string]string); ok {
			if pattern, ok := specs["$pattern"]; ok {
				mongoPattern := toMongoPattern(pattern)
				mgf[key] = bson.M{
					"$regex": mongoPattern,
				}
				continue
			}
			return nil, fmt.Errorf("unknown filter specification - supported type is $pattern")
		}
		// if filter key contains multiple values to search by
		if val, ok := value.(string); ok {
			if values := strings.Split(val, ","); len(values) > 1 {
				mgf[key] = bson.M{
					"$in": values,
				}
				continue
			}
		}
		// if filter _id contains multiple id to search by
		if val, ok := value.([]bson.ObjectId); ok {
			if len(val) > 1 {
				mgf[key] = bson.M{
					"$in": val,
				}
				continue
			}
		}
		mgf[key] = value // copy over the key=>value pairs to do exact matching
	}
	return mgf, nil
}

func toMongoPattern(pattern string) string {
	mongoPattern := ""

	prev := '\000'

	for _, r := range pattern {
		if r == '%' {
			if prev == '%' {
				mongoPattern += "%"
				prev = '\000'
				continue
			}
			prev = r
			continue
		}
		if prev == '%' {
			mongoPattern += ".*"
		}
		if r != '\000' {
			mongoPattern += string(r)
		}

		prev = r
	}
	if prev == '%' {
		// at the very end of the pattern
		mongoPattern += ".*"
	}

	if !strings.HasPrefix(mongoPattern, ".*") {
		mongoPattern = "^" + mongoPattern
	}
	if !strings.HasSuffix(mongoPattern, ".*") {
		mongoPattern = mongoPattern + "$"
	}
	return mongoPattern
}
