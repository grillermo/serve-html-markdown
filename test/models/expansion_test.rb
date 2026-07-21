require "test_helper"

class ExpansionTest < ActiveSupport::TestCase
  setup do
    @user = User.create!(email: "expansion-model@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", occurrence: 0,
      question: "Why?", use_openai: false
    )
  end

  test "starts pending and requires all submitted fields" do
    assert_equal "pending", @expansion.status
    invalid = @user.expansions.build(file_name: "", selected_text: "", question: "")
    assert_not invalid.valid?
  end

  test "claims a pending job only once" do
    assert @expansion.claim!
    assert_equal "processing", @expansion.reload.status
    assert_not @expansion.claim!
  end

  test "records completed and failed terminal states" do
    @expansion.claim!
    @expansion.complete!("/notes--expand-1.html")
    assert_equal ["completed", "/notes--expand-1.html", nil],
      @expansion.reload.attributes.values_at("status", "url", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "Why?")
    failed.claim!
    failed.fail!("Generation failed.")
    assert_equal ["failed", nil, "Generation failed."],
      failed.reload.attributes.values_at("status", "url", "error_detail")
  end

  test "stamp! records a stage as epoch milliseconds and defaults timings to an empty hash" do
    assert_equal({}, @expansion.timings)

    before = Expansion.now_ms
    @expansion.stamp!(:job_started)
    after = Expansion.now_ms

    recorded = @expansion.reload.timings["job_started"]
    assert_kind_of Integer, recorded
    assert_operator recorded, :>=, before
    assert_operator recorded, :<=, after
  end

  test "stamp! accepts an explicit epoch_ms for client-provided timestamps" do
    @expansion.stamp!(:client_clicked, 1_700_000_000_000)

    assert_equal 1_700_000_000_000, @expansion.reload.timings["client_clicked"]
  end

  test "stamp! merges stages without clobbering earlier ones, including concurrent writers" do
    @expansion.stamp!(:request_received, 1)

    threads = [
      Thread.new { Expansion.find(@expansion.id).stamp!(:job_started, 2) },
      Thread.new { Expansion.find(@expansion.id).stamp!(:source_read, 3) }
    ]
    threads.each(&:join)

    timings = @expansion.reload.timings
    assert_equal({ "request_received" => 1, "job_started" => 2, "source_read" => 3 }, timings)
  end

  test "stamp! rescues and logs instead of raising when save fails" do
    @expansion.define_singleton_method(:save!) { |*| raise ActiveRecord::RecordInvalid.new(self) }

    assert_nothing_raised { @expansion.stamp!(:job_started) }
  end
end
