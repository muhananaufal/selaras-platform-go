package outbox

import "fmt"

// TopicFor memetakan jenis event ke topic Kafka-nya.
//
// Pemetaannya eksplisit dan tanpa jalur bawaan. Event yang tidak dikenali
// menjadi galat, bukan diarahkan ke topic serbaguna: event yang mendarat di
// topic yang salah akan dibaca konsumen yang tidak mengharapkannya, dan
// kegagalannya muncul jauh dari sebabnya.
func TopicFor(eventType string) (string, error) {
	switch eventType {
	case EventProfileUpdated:
		return TopicProfileUpdated, nil

	case EventAssessmentCompleted:
		return TopicAssessmentCompleted, nil

	case EventCoachingProgramUpdate:
		return TopicCoachingProgram, nil

	// Seluruh permintaan pekerjaan LLM berbagi satu topic. Mereka dikerjakan
	// armada worker yang sama dan bersaing memperebutkan kuota penyedia yang
	// sama, jadi memisahkannya per jenis hanya akan membagi antrean yang
	// sebetulnya satu.
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
