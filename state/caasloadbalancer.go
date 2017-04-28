package state

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/juju/errors"
	statetxn "github.com/juju/txn"
	"gopkg.in/juju/names.v2"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/mgo.v2/txn"

	"github.com/juju/juju/network"
)

type caasLoadBalancerDoc struct {
	DocID           string      `bson:"_id"`
	ModelUUID       string      `bson:"model-uuid"`
	Name            string      `bson:"name"`
	CAASApplication string      `bson:"caasapplication"`
	Ports           []PortRange `bson:"ports"`
	TxnRevno        int64       `bson:"txn-revno"`
}

type CAASLoadBalancer struct {
	st  *State
	doc caasLoadBalancerDoc
}

func (clb *CAASLoadBalancer) globalKey() string {
	return caasLoadBalancerGlobalKey(clb.doc.CAASApplication.Name)
}

func caasLoadBalancerGlobalKey(application string) string {
	return fmt.Sprintf("clb#%s", application)
}

func getCAASLoadBalancer(st *State, application string) (*Ports, error) {
	loadBalancers, closer := st.db().GetCollection(caasLoadBalancerC)
	defer closer()

	var doc loadBalancerDoc
	key := caasLoadBalancerGlobalKey(application)
	err := openedPorts.FindId(key).One(&doc)
	if err != nil {
		doc.CAASApplication = application
		clb := CAASLoadBalancer{st, doc, false}
		if err == mgo.ErrNotFound {
			return nil, errors.NotFoundf(p.String())
		}
		return nil, errors.Annotatef(err, "cannot get %s", p.String())
	}

	return &Ports{st, doc, false}, nil
}

func getOrCreateCAASLoadBalancer(st *State, application string) (*CAASLoadBalancer, error) {
	clb, err := getCAASLoadBalancer(st, application)
	if errors.IsNotFound(err) {
		key := caasLoadBalancerGlobalKey(application)
		doc := caasLoadBalancerDoc{
			DocID:           st.docID(key),
			CAASApplication: application,
			ModelUUID:       st.ModelUUID(),
		}
		clb = &CAASLoadBalancer{st, doc}
	} else if err != nil {
		return nil, errors.Trace(err)
	}
	return clb, nil
}
