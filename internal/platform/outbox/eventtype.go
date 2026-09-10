package outbox

// Event kinds, named once.
//
// Three of them happen to share their topic's name, and that coincidence is
// what makes the literal repeat. Naming them here keeps "event kind" and
// "topic name" two different things even when their values agree - if one of
// them changes later, only one side changes.
const (
	EventProfileUpdated        = "profile.updated"
	EventAssessmentCompleted   = "assessment.completed"
	EventCoachingProgramUpdate = "coaching.program.updated"

	EventPersonalizationRequested = "personalization.requested"
	EventCurriculumRequested      = "curriculum.requested"
	EventChatReplyRequested       = "chat.reply.requested"
	EventMealGuideRequested       = "meal.guide.requested"

	EventPersonalizationCompleted = "personalization.completed"
	EventCurriculumCompleted      = "curriculum.completed"
	EventChatReplyCompleted       = "chat.reply.completed"
	EventMealGuideCompleted       = "meal.guide.completed"

	EventLLMJobFailed = "llm.job.failed"

	EventUserDeletionRequested = "user.deletion.requested"
	EventUserDeletionConfirmed = "user.deletion.confirmed"
)

// Topic names. Deliberately separate from the event kinds - see above.
const (
	TopicProfileUpdated      = "profile.updated"
	TopicAssessmentCompleted = "assessment.completed"
	TopicCoachingProgram     = "coaching.program.updated"
	TopicLLMJobs             = "llm.jobs"
	TopicLLMResults          = "llm.results"
	TopicLLMDeadLetter       = "llm.dlq"
	TopicUserDeletion        = "user.deletion"
)
