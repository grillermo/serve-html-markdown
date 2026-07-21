require "test_helper"

class GenerateExpansionJobTest < ActiveJob::TestCase
  setup do
    @user = User.create!(email: "job@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?")
  end

  test "completes a pending expansion once" do
    with_processor(->(_) { "/notes--expand-1.html" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
      GenerateExpansionJob.perform_now(@expansion.id)
    end

    assert_equal ["completed", "/notes--expand-1.html"], @expansion.reload.attributes.values_at("status", "url")
  end

  test "stores safe details for known and unexpected failures" do
    with_processor(->(_) { raise ClaudeExpandService::Error, "token leaked" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
    end
    assert_equal ["failed", "Generation failed."], @expansion.reload.attributes.values_at("status", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "why?")
    with_processor(->(_) { raise SelectionLinker::NotFound, "Selection not found in source — select a plainer run of text." }) do
      GenerateExpansionJob.perform_now(failed.id)
    end
    assert_equal ["failed", "Selection not found in source — select a plainer run of text."], failed.reload.attributes.values_at("status", "error_detail")
  end

  private

  def with_processor(callable)
    original = ExpansionProcessor.method(:process)
    ExpansionProcessor.define_singleton_method(:process, &callable)
    yield
  ensure
    ExpansionProcessor.define_singleton_method(:process, original)
  end
end
