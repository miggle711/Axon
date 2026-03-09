package queue



type Job struct {
	// Defines the structure of a job in the queue system.

	
	ID	   string `json:"id"` // uuid for unique identification of the job
	Type   string `json:"type"` // "search", "llm_call", "code_exec". Worker uses this to know how to execute. 
	Payload string `json:"payload"` // JSON string containing the data needed to execute the job. The structure of this data can vary based on the job type.

	Status  string `json:"status"` // "pending", "in_progress", "completed", "failed". Used to track the state of the job.
	Priority int    `json:"priority"` // higher number means higher priority. Workers should process higher priority jobs first.

	Retries int `json:"retries"` // number of times the job has been retried after a failure
	MaxRetries int `json:"max_retries"` // maximum number of retries allowed for the job before it is marked as failed; prevents infinite retry loops

	// use a 64-bit int to avoid the 2038 problem with 32-bit timestamps.
	CreatedAt int64 `json:"created_at"` // timestamp when the job was created
	RunAt int64 `json:"run_at"`  // timestamp when the job is scheduled to run


}