package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
	profilegrpc "github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/grpc"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/app"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

func newClient(t *testing.T) (profilev1.ProfileClient, string) {
	t.Helper()

	pool := pgtest.Open(t, "profile")
	pgtest.Truncate(t, pool, "user_profiles")

	svc, err := app.NewService(postgres.NewProfileRepository(pool), time.Now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	server, err := profilegrpc.NewServer(svc)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	profilev1.RegisterProfileServer(grpcServer, server)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Errorf("serving: %v", err)
		}
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing connection: %v", err)
		}
	})

	userID, err := domain.NewUserID()
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	return profilev1.NewProfileClient(conn), userID.String()
}

func ptr(s string) *string { return &s }

// Closes B6 at the contract layer. An empty profile sent over the wire MUST
// carry absent fields as absent - not as empty strings, and certainly not as
// today's date.
func TestAnEmptyProfileCrossesTheWireAsAbsent(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	created, err := client.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{UserId: userID})
	if err != nil {
		t.Fatalf("CreateEmptyProfile: %v", err)
	}

	p := created.GetProfile()
	if p.DateOfBirth != nil {
		t.Errorf("date of birth = %q; want it absent", p.GetDateOfBirth())
	}
	if p.FirstName != nil || p.LastName != nil || p.CountryOfResidence != nil {
		t.Errorf("empty fields were sent as present: %+v", p)
	}
	if p.GetSex() != profilev1.Sex_SEX_UNSPECIFIED {
		t.Errorf("sex = %v; want SEX_UNSPECIFIED", p.GetSex())
	}
	if p.GetLanguage() != "id" {
		t.Errorf("language = %q; want the default", p.GetLanguage())
	}
	if p.GetTimestamps().GetCreatedAt() == nil {
		t.Error("timestamps were not sent")
	}
}

func TestCreatingAProfileTwiceReturnsTheSameOne(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	first, err := client.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{UserId: userID})
	if err != nil {
		t.Fatalf("first CreateEmptyProfile: %v", err)
	}
	// identity-svc calls this best-effort and may retry after an answer lost
	// on the network; the second attempt has to produce the same thing, not an
	// error.
	second, err := client.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{UserId: userID})
	if err != nil {
		t.Fatalf("second CreateEmptyProfile: %v", err)
	}
	if first.GetProfile().GetId() != second.GetProfile().GetId() {
		t.Error("a retry created a second profile")
	}
}

// ADR-022. A user whose profile failed to be created at registration has to
// be able to create it through an ordinary update; otherwise they are locked
// out forever - even though ADR-002 rule 1 says that state may happen.
func TestUpdatingWithoutAProfileCreatesOne(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	if _, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: userID}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetProfile = %v; want NotFound before anything exists", status.Code(err))
	}

	updated, err := client.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId:    userID,
		FirstName: ptr("Sri"),
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.GetProfile().GetFirstName() != "Sri" {
		t.Errorf("first name = %q; want %q", updated.GetProfile().GetFirstName(), "Sri")
	}

	found, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: userID})
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if found.GetProfile().GetId() != updated.GetProfile().GetId() {
		t.Error("the profile created by the update is not the one that comes back")
	}
}

func TestAPartialUpdateLeavesTheRestAlone(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	if _, err := client.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId:             userID,
		FirstName:          ptr("Sri"),
		LastName:           ptr("Wahyuni"),
		DateOfBirth:        ptr("1990-05-17"),
		Sex:                profilev1.Sex_SEX_FEMALE,
		CountryOfResidence: ptr("Indonesia"),
		Language:           ptr("en"),
	}); err != nil {
		t.Fatalf("first UpdateProfile: %v", err)
	}

	// Only the country is sent. Nothing else may be touched.
	got, err := client.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId:             userID,
		CountryOfResidence: ptr("Malaysia"),
	})
	if err != nil {
		t.Fatalf("second UpdateProfile: %v", err)
	}

	p := got.GetProfile()
	if p.GetCountryOfResidence() != "Malaysia" {
		t.Errorf("country = %q; want Malaysia", p.GetCountryOfResidence())
	}
	if p.GetFirstName() != "Sri" || p.GetLastName() != "Wahyuni" {
		t.Errorf("names were lost: %q %q", p.GetFirstName(), p.GetLastName())
	}
	if p.GetDateOfBirth() != "1990-05-17" {
		t.Errorf("date of birth = %q; want it kept", p.GetDateOfBirth())
	}
	if p.GetSex() != profilev1.Sex_SEX_FEMALE {
		t.Errorf("sex = %v; want it kept", p.GetSex())
	}
	if p.GetLanguage() != "en" {
		t.Errorf("language = %q; want it kept", p.GetLanguage())
	}
}

func TestValuesTheRiskEngineCannotUseAreRefused(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	for name, req := range map[string]*profilev1.UpdateProfileRequest{
		"future birth date": {UserId: userID, DateOfBirth: ptr("2099-01-01")},
		"malformed date":    {UserId: userID, DateOfBirth: ptr("17/05/1990")},
		"unknown language":  {UserId: userID, Language: ptr("fr")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.UpdateProfile(ctx, req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v; want InvalidArgument", status.Code(err))
			}
		})
	}

	// Not one of them may leave a half-made profile behind.
	if _, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: userID}); status.Code(err) != codes.NotFound {
		t.Errorf("a rejected update left a profile behind: %v", status.Code(err))
	}
}

// ADR-002 rule 2: a profile that does not exist yet means empty claims, not
// an error. identity-svc calls this when issuing a token, and failing it
// would turn a valid state into a user who cannot sign in.
func TestResolvingAProfileThatDoesNotExistIsNotAnError(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	got, err := client.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{UserId: userID})
	if err != nil {
		t.Fatalf("ResolveProfileId = %v; want success with an empty id", err)
	}
	if got.GetUserProfileId() != "" {
		t.Errorf("user profile id = %q; want empty", got.GetUserProfileId())
	}

	created, err := client.CreateEmptyProfile(ctx, &profilev1.CreateEmptyProfileRequest{UserId: userID})
	if err != nil {
		t.Fatalf("CreateEmptyProfile: %v", err)
	}
	got, err = client.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{UserId: userID})
	if err != nil {
		t.Fatalf("ResolveProfileId: %v", err)
	}
	if got.GetUserProfileId() != created.GetProfile().GetId() {
		t.Errorf("resolved %q; want %q", got.GetUserProfileId(), created.GetProfile().GetId())
	}
}

func TestAMalformedUserIdIsInvalidArgument(t *testing.T) {
	client, _ := newClient(t)
	ctx := context.Background()

	if _, err := client.GetProfile(ctx, &profilev1.GetProfileRequest{UserId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("GetProfile code = %v; want InvalidArgument", status.Code(err))
	}
	if _, err := client.ResolveProfileId(ctx, &profilev1.ResolveProfileIdRequest{UserId: ""}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("ResolveProfileId code = %v; want InvalidArgument", status.Code(err))
	}
}

// A field sent empty means deliberately cleared, and that has to reach the
// database as NULL - and come back as absent.
func TestAnExplicitEmptyValueClearsAFieldOverTheWire(t *testing.T) {
	client, userID := newClient(t)
	ctx := context.Background()

	if _, err := client.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId: userID, FirstName: ptr("Sri"),
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	got, err := client.UpdateProfile(ctx, &profilev1.UpdateProfileRequest{
		UserId: userID, FirstName: ptr(""),
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if got.GetProfile().FirstName != nil {
		t.Errorf("first name = %q; want it cleared and absent", got.GetProfile().GetFirstName())
	}
}
