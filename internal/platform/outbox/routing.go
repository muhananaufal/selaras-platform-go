package outbox

import "fmt"

// TopicFor maps an event kind to its Kafka topic.
//
// The mapping is explicit and has no default branch. An unrecognised event
// becomes an error, not a message steered to a catch-all topic: an event
// landing on the wrong topic is read by a consumer that does not expect it,
// and the failure shows up far from its cause.
func TopicFor(eventType string) (string, error) {
	switch eventType {
	case EventProfileUpdated:
		return TopicProfileUpdated, nil

	case EventAssessmentCompleted:
		return TopicAssessmentCompleted, nil

	case EventCoachingProgramUpdate:
		return TopicCoachingProgram, nil

	// Every LLM job request shares one topic. They are worked by the same
	// worker fleet and compete for the same provider quota, so splitting them
	// per kind would only divide a queue that is really one.
	case EventPersonalizationRequested,
		EventCurriculumRequested,
		EventChatReplyRequested,
		EventMealGuideRequested:
		return TopicLLMJobs, nil

	case EventPersonalizationCompleted,
		EventCurriculumCompleted,
		EventChatReplyCompleted,
		EventMealGuideCompleted:
		return TopicLLMResults, nil

	case EventLLMJobFailed:
		return TopicLLMDeadLetter, nil

	case EventUserDeletionRequested,
		EventUserDeletionConfirmed:
		return TopicUserDeletion, nil

	default:
		return "", fmt.Errorf("no topic is defined for event type %q", eventType)
	}
}
