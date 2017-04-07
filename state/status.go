// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"time"

	"github.com/juju/errors"
	jujutxn "github.com/juju/txn"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/mgo.v2/txn"

	"github.com/juju/juju/core/leadership"
	"github.com/juju/juju/mongo"
	"github.com/juju/juju/mongo/utils"
	"github.com/juju/juju/status"
)

// statusDoc represents a entity status in Mongodb.  The implicit
// _id field is explicitly set to the global key of the associated
// entity in the document's creation transaction, but omitted to allow
// direct use of the document in both create and update transactions.
type statusDoc struct {
	ModelUUID  string                 `bson:"model-uuid"`
	Status     status.Status          `bson:"status"`
	StatusInfo string                 `bson:"statusinfo"`
	StatusData map[string]interface{} `bson:"statusdata"`

	// Updated used to be a *time.Time that was not present on statuses dating
	// from older versions of juju so this might be 0 for those cases.
	Updated int64 `bson:"updated"`

	// TODO(fwereade/wallyworld): lp:1479278
	// NeverSet is a short-term hack to work around a misfeature in service
	// status. To maintain current behaviour, we create service status docs
	// (and only service status documents) with NeverSet true; and then, when
	// reading them, if NeverSet is still true, we aggregate status from the
	// units instead.
	NeverSet bool `bson:"neverset"`
}

func unixNanoToTime(i int64) *time.Time {
	t := time.Unix(0, i)
	return &t
}

// getStatus retrieves the status document associated with the given
// globalKey and converts it to a StatusInfo. If the status document
// is not found, a NotFoundError referencing badge will be returned.
func getStatus(mb modelBackend, globalKey, badge string) (_ status.StatusInfo, err error) {
	defer errors.DeferredAnnotatef(&err, "cannot get status")
	statuses, closer := mb.db().GetCollection(statusesC)
	defer closer()

	var doc statusDoc
	err = statuses.FindId(globalKey).One(&doc)
	if err == mgo.ErrNotFound {
		return status.StatusInfo{}, errors.NotFoundf(badge)
	} else if err != nil {
		return status.StatusInfo{}, errors.Trace(err)
	}

	return status.StatusInfo{
		Status:  doc.Status,
		Message: doc.StatusInfo,
		Data:    utils.UnescapeKeys(doc.StatusData),
		Since:   unixNanoToTime(doc.Updated),
	}, nil
}

// setStatusParams configures a setStatus call. All parameters are presumed to
// be set to valid values unless otherwise noted.
type setStatusParams struct {

	// badge is used to specialize any NotFound error emitted.
	badge string

	// globalKey uniquely identifies the entity to which the
	globalKey string

	// status is the status value.
	status status.Status

	// message is an optional string elaborating upon the status.
	message string

	// rawData is a map of arbitrary data elaborating upon the status and
	// message. Its keys are assumed not to have been escaped.
	rawData map[string]interface{}

	// token, if present, must accept an *[]txn.Op passed to its Check method,
	// and will prevent any change if it becomes invalid.
	token leadership.Token

	// udpated, the time the status was set.
	updated *time.Time
}

// setStatus inteprets the supplied params as documented on the type.
func setStatus(st modelBackend, params setStatusParams) (err error) {
	defer errors.DeferredAnnotatef(&err, "cannot set status")
	if params.updated == nil {
		now := st.modelClock().Now()
		params.updated = &now
	}
	doc := statusDoc{
		Status:     params.status,
		StatusInfo: params.message,
		StatusData: utils.EscapeKeys(params.rawData),
		Updated:    params.updated.UnixNano(),
	}
	probablyUpdateStatusHistory(st, params.globalKey, doc)

	// Set the authoritative status document, or fail trying.
	var buildTxn jujutxn.TransactionSource = func(int) ([]txn.Op, error) {
		return statusSetOps(st, doc, params.globalKey)
	}
	if params.token != nil {
		buildTxn = buildTxnWithLeadership(buildTxn, params.token)
	}
	err = st.db().Run(buildTxn)
	if cause := errors.Cause(err); cause == mgo.ErrNotFound {
		return errors.NotFoundf(params.badge)
	}
	return errors.Trace(err)
}

func statusSetOps(st modelBackend, doc statusDoc, globalKey string) ([]txn.Op, error) {
	update := bson.D{{"$set", &doc}}
	txnRevno, err := readTxnRevno(st, statusesC, globalKey)
	if err != nil {
		return nil, errors.Trace(err)
	}
	assert := bson.D{{"txn-revno", txnRevno}}
	return []txn.Op{{
		C:      statusesC,
		Id:     globalKey,
		Assert: assert,
		Update: update,
	}}, nil
}

// createStatusOp returns the operation needed to create the given status
// document associated with the given globalKey.
func createStatusOp(st modelBackend, globalKey string, doc statusDoc) txn.Op {
	return txn.Op{
		C:      statusesC,
		Id:     st.docID(globalKey),
		Assert: txn.DocMissing,
		Insert: &doc,
	}
}

// removeStatusOp returns the operation needed to remove the status
// document associated with the given globalKey.
func removeStatusOp(backend modelBackend, globalKey string) txn.Op {
	return txn.Op{
		C:      statusesC,
		Id:     backend.docID(globalKey),
		Remove: true,
	}
}

type historicalStatusDoc struct {
	ModelUUID  string                 `bson:"model-uuid"`
	GlobalKey  string                 `bson:"globalkey"`
	Status     status.Status          `bson:"status"`
	StatusInfo string                 `bson:"statusinfo"`
	StatusData map[string]interface{} `bson:"statusdata"`

	// Updated might not be present on statuses copied by old
	// versions of juju from yet older versions of juju.
	Updated int64 `bson:"updated"`
}

func probablyUpdateStatusHistory(mb modelBackend, globalKey string, doc statusDoc) {
	historyDoc := &historicalStatusDoc{
		Status:     doc.Status,
		StatusInfo: doc.StatusInfo,
		StatusData: doc.StatusData, // coming from a statusDoc, already escaped
		Updated:    doc.Updated,
		GlobalKey:  globalKey,
	}
	history, closer := mb.db().GetCollection(statusesHistoryC)
	defer closer()
	historyW := history.Writeable()
	if err := historyW.Insert(historyDoc); err != nil {
		logger.Errorf("failed to write status history: %v", err)
	}
}

// statusHistoryArgs hold the arguments to call statusHistory.
type statusHistoryArgs struct {
	st        modelBackend
	globalKey string
	filter    status.StatusHistoryFilter
}

// fetchNStatusResults will return status for the given key filtered with the
// given filter or error.
func fetchNStatusResults(col mongo.Collection, key string,
	filter status.StatusHistoryFilter) ([]historicalStatusDoc, error) {
	var (
		docs  []historicalStatusDoc
		query mongo.Query
	)
	baseQuery := bson.M{"globalkey": key}
	if filter.Delta != nil {
		delta := *filter.Delta
		// TODO(perrito666) 2016-10-06 lp:1558657
		updated := time.Now().Add(-delta)
		baseQuery["updated"] = bson.M{"$gt": updated.UnixNano()}
	}
	if filter.FromDate != nil {
		baseQuery["updated"] = bson.M{"$gt": filter.FromDate.UnixNano()}
	}
	excludes := []string{}
	excludes = append(excludes, filter.Exclude.Values()...)
	if len(excludes) > 0 {
		baseQuery["statusinfo"] = bson.M{"$nin": excludes}
	}

	query = col.Find(baseQuery).Sort("-updated")
	if filter.Size > 0 {
		query = query.Limit(filter.Size)
	}
	err := query.All(&docs)

	if err == mgo.ErrNotFound {
		return []historicalStatusDoc{}, errors.NotFoundf("status history")
	} else if err != nil {
		return []historicalStatusDoc{}, errors.Annotatef(err, "cannot get status history")
	}
	return docs, nil

}

func statusHistory(args *statusHistoryArgs) ([]status.StatusInfo, error) {
	if err := args.filter.Validate(); err != nil {
		return nil, errors.Annotate(err, "validating arguments")
	}
	statusHistory, closer := args.st.db().GetCollection(statusesHistoryC)
	defer closer()

	var results []status.StatusInfo
	docs, err := fetchNStatusResults(statusHistory, args.globalKey, args.filter)
	partial := []status.StatusInfo{}
	if err != nil {
		return []status.StatusInfo{}, errors.Trace(err)
	}
	for _, doc := range docs {
		partial = append(partial, status.StatusInfo{
			Status:  doc.Status,
			Message: doc.StatusInfo,
			Data:    utils.UnescapeKeys(doc.StatusData),
			Since:   unixNanoToTime(doc.Updated),
		})
	}
	results = partial
	return results, nil
}

// PruneStatusHistory removes status history entries until
// only logs newer than <maxLogTime> remain and also ensures
// that the collection is smaller than <maxLogsMB> after the
// deletion.
func PruneStatusHistory(st *State, maxHistoryTime time.Duration, maxHistoryMB int) error {
	if maxHistoryMB < 0 {
		return errors.NotValidf("non-positive maxHistoryMB")
	}
	if maxHistoryTime < 0 {
		return errors.NotValidf("non-positive maxHistoryTime")
	}
	if maxHistoryMB == 0 && maxHistoryTime == 0 {
		return errors.NotValidf("backlog size and time constraints are both 0")
	}

	// NOTE(axw) we require a raw collection to obtain the size of the
	// collection. Take care to include model-uuid in queries where
	// appropriate.
	history, closer := st.getRawCollection(statusesHistoryC)
	defer closer()

	// Status Record Age
	if maxHistoryTime > 0 {
		t := st.clock.Now().Add(-maxHistoryTime)
		_, err := history.RemoveAll(bson.D{
			{"model-uuid", st.ModelUUID()},
			{"updated", bson.M{"$lt": t.UnixNano()}},
		})
		if err != nil {
			return errors.Trace(err)
		}
	}
	if maxHistoryMB == 0 {
		return nil
	}
	// Collection Size
	collMB, err := getCollectionMB(history)
	if err != nil {
		return errors.Annotate(err, "retrieving status history collection size")
	}
	if collMB <= maxHistoryMB {
		return nil
	}
	// TODO(perrito666) explore if there would be any beneffit from having the
	// size limit be per model
	count, err := history.Count()
	if err == mgo.ErrNotFound || count <= 0 {
		return nil
	}
	if err != nil {
		return errors.Annotate(err, "counting status history records")
	}
	// We are making the assumption that status sizes can be averaged for
	// large numbers and we will get a reasonable approach on the size.
	// Note: Capped collections are not used for this because they, currently
	// at least, lack a way to be resized and the size is expected to change
	// as real life data of the history usage is gathered.
	sizePerStatus := float64(collMB) / float64(count)
	if sizePerStatus == 0 {
		return errors.New("unexpected result calculating status history entry size")
	}
	deleteStatuses := count - int(float64(collMB-maxHistoryMB)/sizePerStatus)
	result := historicalStatusDoc{}
	err = history.Find(nil).Sort("-updated").Skip(deleteStatuses).One(&result)
	if err != nil {
		return errors.Trace(err)
	}
	_, err = history.RemoveAll(bson.D{
		{"updated", bson.M{"$lt": result.Updated}},
	})
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}
