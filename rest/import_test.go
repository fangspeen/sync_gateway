package rest

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/couchbase/sync_gateway/base"
	"github.com/couchbase/sync_gateway/db"
	"github.com/couchbaselabs/go.assert"
)

func SkipImportTestsIfNotEnabled(t *testing.T) {

	if !base.TestUseXattrs() {
		t.Skip("XATTR based tests not enabled.  Enable via SG_TEST_USE_XATTRS=true environment variable")
	}

	if base.UnitTestUrlIsWalrus() {
		t.Skip("This test won't work under walrus until https://github.com/couchbase/sync_gateway/issues/2390")
	}
}

type simpleSync struct {
	Channels map[string]interface{}
	Rev      string
}

type rawResponse struct {
	Sync simpleSync `json:"_sync"`
}

func hasActiveChannel(channelSet map[string]interface{}, channelName string) bool {
	if channelSet == nil {
		return false
	}
	value, ok := channelSet[channelName]
	if !ok || value != nil { // An entry for the channel name with a nil value represents an active channel
		return false
	}

	return true
}

// Test import of an SDK delete.
func TestXattrImportOldDoc(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) {
			if (oldDoc == null) {
				channel("oldDocNil")
			} 
			if (doc._deleted) {
				channel("docDeleted")
			}
		}`}
	defer rt.Close()

	bucket := rt.Bucket()
	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true}`)

	// 1. Test oldDoc behaviour during SDK insert
	key := "TestImportDelete"
	docBody := make(map[string]interface{})
	docBody["test"] = "TestImportDelete"
	docBody["channels"] = "ABC"

	_, err := bucket.Add(key, 0, docBody)
	assertNoError(t, err, "Unable to insert doc TestImportDelete")

	// Attempt to get the document via Sync Gateway, to trigger import.  On import of a create, oldDoc should be nil.
	response := rt.SendAdminRequest("GET", "/db/_raw/TestImportDelete", "")
	assert.Equals(t, response.Code, 200)
	var rawInsertResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawInsertResponse)
	assertNoError(t, err, "Unable to unmarshal raw response")
	assertTrue(t, rawInsertResponse.Sync.Channels != nil, "Expected channels not returned for SDK insert")
	log.Printf("insert channels: %+v", rawInsertResponse.Sync.Channels)
	assertTrue(t, hasActiveChannel(rawInsertResponse.Sync.Channels, "oldDocNil"), "oldDoc was not nil during import of SDK insert")

	// 2. Test oldDoc behaviour during SDK update

	updatedBody := make(map[string]interface{})
	updatedBody["test"] = "TestImportDelete"
	updatedBody["channels"] = "HBO"

	err = bucket.Set(key, 0, updatedBody)
	assertNoError(t, err, "Unable to update doc TestImportDelete")

	// Attempt to get the document via Sync Gateway, to trigger import.  On import of a create, oldDoc should be nil.
	response = rt.SendAdminRequest("GET", "/db/_raw/TestImportDelete", "")
	assert.Equals(t, response.Code, 200)
	var rawUpdateResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawUpdateResponse)
	assertNoError(t, err, "Unable to unmarshal raw response")
	assertTrue(t, rawUpdateResponse.Sync.Channels != nil, "Expected channels not returned for SDK update")
	log.Printf("update channels: %+v", rawUpdateResponse.Sync.Channels)
	assertTrue(t, hasActiveChannel(rawUpdateResponse.Sync.Channels, "oldDocNil"), "oldDoc was not nil during import of SDK update")

	// 3. Test oldDoc behaviour during SDK delete
	err = bucket.Delete(key)
	assertNoError(t, err, "Unable to delete doc TestImportDelete")

	response = rt.SendAdminRequest("GET", "/db/_raw/TestImportDelete", "")
	assert.Equals(t, response.Code, 200)
	var rawDeleteResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawDeleteResponse)
	log.Printf("Post-delete: %s", response.Body.Bytes())
	assertNoError(t, err, "Unable to unmarshal raw response")
	assertTrue(t, hasActiveChannel(rawDeleteResponse.Sync.Channels, "oldDocNil"), "oldDoc was not nil during import of SDK delete")
	assertTrue(t, hasActiveChannel(rawDeleteResponse.Sync.Channels, "docDeleted"), "doc did not set _deleted:true for SDK delete")

}

// Validate tombstone w/ xattrs
func TestXattrSGTombstone(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true}`)

	// 1. Create doc through SG
	key := "TestXattrSGTombstone"
	docBody := make(map[string]interface{})
	docBody["test"] = key
	docBody["channels"] = "ABC"

	response := rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", key), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	var body db.Body
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId := body["rev"].(string)

	// 2. Delete the doc through SG
	response = rt.SendAdminRequest("DELETE", fmt.Sprintf("/db/%s?rev=%s", key, revId), "")
	assert.Equals(t, response.Code, 200)
	log.Printf("delete response: %s", response.Body.Bytes())
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId = body["rev"].(string)

	// 3. Attempt to retrieve the doc through the SDK
	deletedValue := make(map[string]interface{})
	_, err := bucket.Get(key, deletedValue)
	assertTrue(t, err != nil, "Expected key not found error trying to retrieve document")

}

// Test cas failure during WriteUpdate, triggering import of SDK write.
// Disabled, as test depends on artificial latency in PutDoc to reliably hit the CAS failure on the SG write.  Scenario fully covered
// by functional test.
func DisableTestXattrImportOnCasFailure(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{}
	defer rt.Close()

	bucket := rt.Bucket()
	rt.SendAdminRequest("PUT", "/_logging", `{"ImportCas":true}`)

	// 1. SG Write
	key := "TestCasFailureImport"
	docBody := make(map[string]interface{})
	docBody["test"] = "TestCasFailureImport"
	docBody["SG_write_count"] = "1"

	response := rt.SendAdminRequest("PUT", "/db/TestCasFailureImport", `{"test":"TestCasFailureImport", "write_type":"SG_1"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	var body db.Body
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["rev"], "1-111c27be37c17f18ae8fe9faa3bb4e0e")
	revId := body["rev"].(string)

	// Attempt a second SG write, to be interrupted by an SDK update.  Should return a conflict
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		response = rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s?rev=%s", key, revId), `{"write_type":"SG_2"}`)
		assert.Equals(t, response.Code, 409)
		log.Printf("SG CAS failure write response: %s", response.Body.Bytes())
		wg.Done()
	}()

	// Concurrent SDK writes for 10 seconds, one per second
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		sdkBody := make(map[string]interface{})
		sdkBody["test"] = "TestCasFailureImport"
		sdkBody["SDK_write_count"] = i
		err := bucket.Set(key, 0, sdkBody)
		assertNoError(t, err, "Unexpected error doing SDK write")
	}

	// wait for SG write to happen
	wg.Wait()

	// Get to see where we ended up
	response = rt.SendAdminRequest("GET", "/db/TestCasFailureImport", "")
	assert.Equals(t, response.Code, 200)
	log.Printf("Final get: %s", response.Body.Bytes())

	// Get raw to see where the rev tree ended up
	response = rt.SendAdminRequest("GET", "/db/_raw/TestCasFailureImport", "")
	assert.Equals(t, response.Code, 200)
	log.Printf("Final get raw: %s", response.Body.Bytes())

}

// Attempt to delete then recreate a document through SG
func TestXattrResurrectViaSG(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true}`)

	// 1. Create and import doc
	key := "TestResurrectViaSG"
	docBody := make(map[string]interface{})
	docBody["test"] = key
	docBody["channels"] = "ABC"

	response := rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", key), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	var body db.Body
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId := body["rev"].(string)

	// 2. Delete the doc through SG
	response = rt.SendAdminRequest("DELETE", fmt.Sprintf("/db/%s?rev=%s", key, revId), "")
	assert.Equals(t, response.Code, 200)
	log.Printf("delete response: %s", response.Body.Bytes())
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId = body["rev"].(string)

	// 3. Recreate the doc through the SG (with different data)
	response = rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s?rev=%s", key, revId), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	assert.Equals(t, body["rev"], "3-70c113f08c6622cd87af68f4f60d12e3")

}

// Attempt to delete then recreate a document through the SDK
func TestXattrResurrectViaSDK(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true}`)

	// 1. Create and import doc
	key := "TestResurrectViaSDK"
	docBody := make(map[string]interface{})
	docBody["test"] = key
	docBody["channels"] = "ABC"

	_, err := bucket.Add(key, 0, docBody)
	assertNoError(t, err, "Unable to insert doc TestResurrectViaSDK")

	// Attempt to get the document via Sync Gateway, to trigger import.  On import of a create, oldDoc should be nil.
	rawPath := fmt.Sprintf("/db/_raw/%s", key)
	response := rt.SendAdminRequest("GET", rawPath, "")
	assert.Equals(t, response.Code, 200)
	var rawInsertResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawInsertResponse)
	assertNoError(t, err, "Unable to unmarshal raw response")

	// 2. Delete the doc through the SDK
	err = bucket.Delete(key)
	assertNoError(t, err, "Unable to delete doc TestResurrectViaSDK")

	response = rt.SendAdminRequest("GET", rawPath, "")
	assert.Equals(t, response.Code, 200)
	var rawDeleteResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawDeleteResponse)
	log.Printf("Post-delete: %s", response.Body.Bytes())
	assertNoError(t, err, "Unable to unmarshal raw response")

	// 3. Recreate the doc through the SDK (with different data)
	updatedBody := make(map[string]interface{})
	updatedBody["test"] = key
	updatedBody["channels"] = "HBO"

	err = bucket.Set(key, 0, updatedBody)
	assertNoError(t, err, "Unable to update doc TestResurrectViaSDK")

	// Attempt to get the document via Sync Gateway, to trigger import.
	response = rt.SendAdminRequest("GET", rawPath, "")
	assert.Equals(t, response.Code, 200)
	var rawUpdateResponse rawResponse
	err = json.Unmarshal(response.Body.Bytes(), &rawUpdateResponse)
	assertNoError(t, err, "Unable to unmarshal raw response")
	_, ok := rawUpdateResponse.Sync.Channels["HBO"]
	assertTrue(t, ok, "Didn't find expected channel (HBO) on resurrected doc")

}

// Attempt to delete a document that's already been deleted via the SDK
func TestXattrDoubleDelete(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true}`)

	// 1. Create and import doc
	key := "TestDoubleDelete"
	docBody := make(map[string]interface{})
	docBody["test"] = key
	docBody["channels"] = "ABC"

	response := rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", key), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	var body db.Body
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId := body["rev"].(string)

	// 2. Delete the doc through the SDK
	log.Printf("...............Delete through SDK.....................................")
	deleteErr := bucket.Delete(key)
	assertNoError(t, deleteErr, "Couldn't delete via SDK")

	log.Printf("...............Delete through SG.......................................")

	// 3. Delete the doc through SG.  Expect a conflict, as the import of the SDK delete will create a new
	//    tombstone revision
	response = rt.SendAdminRequest("DELETE", fmt.Sprintf("/db/%s?rev=%s", key, revId), "")
	assert.Equals(t, response.Code, 409)
	log.Printf("delete response: %s", response.Body.Bytes())
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId = body["rev"].(string)

}

func TestViewQueryTombstoneRetrieval(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true}`)

	// 1. Create and import docs
	key := "SG_delete"
	docBody := make(map[string]interface{})
	docBody["test"] = key
	docBody["channels"] = "ABC"

	response := rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", key), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	var body db.Body
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId := body["rev"].(string)

	sdk_key := "SDK_delete"
	docBody = make(map[string]interface{})
	docBody["test"] = sdk_key
	docBody["channels"] = "ABC"

	response = rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", sdk_key), `{"channels":"ABC"}`)
	assert.Equals(t, response.Code, 201)
	log.Printf("insert response: %s", response.Body.Bytes())
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)

	// 2. Delete SDK_delete through the SDK
	log.Printf("...............Delete through SDK.....................................")
	deleteErr := bucket.Delete(sdk_key)
	assertNoError(t, deleteErr, "Couldn't delete via SDK")

	// Trigger import via SG retrieval
	response = rt.SendAdminRequest("GET", fmt.Sprintf("/db/%s", sdk_key), "")
	assert.Equals(t, response.Code, 404) // expect 404 deleted

	// 3.  Delete SG_delete through SG.
	log.Printf("...............Delete through SG.......................................")
	response = rt.SendAdminRequest("DELETE", fmt.Sprintf("/db/%s?rev=%s", key, revId), "")
	assert.Equals(t, response.Code, 200)
	log.Printf("delete response: %s", response.Body.Bytes())
	json.Unmarshal(response.Body.Bytes(), &body)
	assert.Equals(t, body["ok"], true)
	revId = body["rev"].(string)

	log.Printf("TestXattrDeleteDCPMutation done")

	// Attempt to retrieve via view.  Above operations were all synchronous (on-demand import of SDK delete, SG delete), so
	// stale=false view results should be immediately updated.
	results, err := rt.GetDatabase().ChannelViewTest("ABC", 1000, db.ChangesOptions{})
	assertNoError(t, err, "Error issuing channel view query")
	for _, entry := range results {
		log.Printf("Got view result: %v", entry)
	}
	assert.Equals(t, len(results), 2)
	assertTrue(t, strings.HasPrefix(results[0].RevID, "2-") && strings.HasPrefix(results[1].RevID, "2-"), "Unexpected revisions in view results post-delete")
}

func TestXattrImportFilterOptIn(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	importFilter := `function (doc) { return doc.type == "mobile"}`
	rt := RestTester{
		SyncFn: `function(doc, oldDoc) { channel(doc.channels) }`,
		DatabaseConfig: &DbConfig{
			ImportFilter: &importFilter,
		},
	}
	defer rt.Close()
	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true}`)

	// 1. Create two docs via the SDK, one matching filter
	mobileKey := "TestImportFilterValid"
	mobileBody := make(map[string]interface{})
	mobileBody["type"] = "mobile"
	mobileBody["channels"] = "ABC"
	_, err := bucket.Add(mobileKey, 0, mobileBody)
	assertNoError(t, err, "Error writing SDK doc")

	nonMobileKey := "TestImportFilterInvalid"
	nonMobileBody := make(map[string]interface{})
	nonMobileBody["type"] = "non-mobile"
	nonMobileBody["channels"] = "ABC"
	_, err = bucket.Add(nonMobileKey, 0, nonMobileBody)
	assertNoError(t, err, "Error writing SDK doc")

	// Attempt to get the documents via Sync Gateway.  Will trigger on-demand import.
	response := rt.SendAdminRequest("GET", "/db/"+mobileKey, "")
	assert.Equals(t, response.Code, 200)
	assertDocProperty(t, response, "type", "mobile")

	response = rt.SendAdminRequest("GET", "/db/"+nonMobileKey, "")
	assert.Equals(t, response.Code, 404)
	assertDocProperty(t, response, "reason", "Not imported")

	// PUT to existing document that hasn't been imported.
	sgWriteBody := `{"type":"whatever I want - I'm writing through SG",
	                 "channels": "ABC"}`
	response = rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", nonMobileKey), sgWriteBody)
	assert.Equals(t, response.Code, 201)
	assertDocProperty(t, response, "id", "TestImportFilterInvalid")
	assertDocProperty(t, response, "rev", "1-25c26cdf9d7771e07f00be1d13f7fb7c")
}

// Write through SG, non-imported SDK write, subsequent SG write
func TestXattrSGWriteOfNonImportedDoc(t *testing.T) {

	SkipImportTestsIfNotEnabled(t)

	importFilter := `function (doc) { return doc.type == "mobile"}`
	rt := RestTester{
		SyncFn: `function(doc, oldDoc) { channel(doc.channels) }`,
		DatabaseConfig: &DbConfig{
			ImportFilter: &importFilter,
		},
	}
	defer rt.Close()

	log.Printf("Starting get bucket....")

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true}`)

	// 1. Create doc via SG
	sgWriteKey := "TestImportFilterSGWrite"
	sgWriteBody := `{"type":"whatever I want - I'm writing through SG",
	                 "channels": "ABC"}`
	response := rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", sgWriteKey), sgWriteBody)
	assert.Equals(t, response.Code, 201)
	assertDocProperty(t, response, "rev", "1-25c26cdf9d7771e07f00be1d13f7fb7c")

	// 2. Update via SDK, not matching import filter.  Will not be available via SG
	nonMobileBody := make(map[string]interface{})
	nonMobileBody["type"] = "non-mobile"
	nonMobileBody["channels"] = "ABC"
	err := bucket.Set(sgWriteKey, 0, nonMobileBody)
	assertNoError(t, err, "Error updating SG doc from SDK ")

	// Attempt to get the documents via Sync Gateway.  Will trigger on-demand import.

	response = rt.SendAdminRequest("GET", "/db/"+sgWriteKey, "")
	assert.Equals(t, response.Code, 404)
	assertDocProperty(t, response, "reason", "Not imported")

	// 3. Rewrite through SG - should treat as new insert
	sgWriteBody = `{"type":"SG client rewrite",
	                 "channels": "NBC"}`
	response = rt.SendAdminRequest("PUT", fmt.Sprintf("/db/%s", sgWriteKey), sgWriteBody)
	assert.Equals(t, response.Code, 201)
	// Validate rev is 1, new rev id
	assertDocProperty(t, response, "rev", "1-b9cdd5d413b572476799930f065657a6")
}

// Test to write a binary document to the bucket, ensure it's not imported.
func TestImportBinaryDoc(t *testing.T) {

	rt := RestTester{SyncFn: `
		function(doc, oldDoc) { channel(doc.channels) }`}
	defer rt.Close()

	log.Printf("Starting get bucket....")

	bucket := rt.Bucket()

	rt.SendAdminRequest("PUT", "/_logging", `{"Import+":true, "CRUD+":true, "Cache+":true}`)

	// 1. Write a binary doc through the SDK
	rawBytes := []byte("some bytes")
	err := bucket.SetRaw("binaryDoc", 0, rawBytes)
	assertNoError(t, err, "Error writing binary doc through the SDK")

	// 2. Ensure we can't retrieve the document via SG
	response := rt.SendAdminRequest("GET", "/db/binaryDoc", "")
	assert.True(t, response.Code != 200)
}

func assertDocProperty(t *testing.T, getDocResponse *TestResponse, propertyName string, expectedPropertyValue interface{}) {
	var responseBody map[string]interface{}
	err := json.Unmarshal(getDocResponse.Body.Bytes(), &responseBody)
	assertNoError(t, err, "Error unmarshalling document response")
	value, ok := responseBody[propertyName]
	assertTrue(t, ok, fmt.Sprintf("Expected property %s not found in response %s", propertyName, getDocResponse.Body.Bytes()))
	assert.Equals(t, value, expectedPropertyValue)
}
