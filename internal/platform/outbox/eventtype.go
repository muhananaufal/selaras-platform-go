package outbox

// Jenis event, dinamai sekali.
//
// Tiga di antaranya kebetulan senama dengan topic-nya, dan kebetulan itu yang
// membuat literalnya berulang. Menamainya di sini membuat "jenis event" dan
// "nama topic" tetap dua hal yang berbeda meski nilainya sama - kalau salah
// satunya berubah nanti, yang berubah hanya satu sisi.
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

// Nama topic. Terpisah dari jenis event dengan sengaja - lihat di atas.
const (
	TopicProfileUpdated      = "profile.updated"
	TopicAssessmentCompleted = "assessment.completed"
	TopicCoachingProgram     = "coaching.program.updated"
	TopicLLMJobs             = "llm.jobs"
	TopicLLMResults          = "llm.results"
	TopicLLMDeadLetter       = "llm.dlq"
	TopicUserDeletion        = "user.deletion"
)
