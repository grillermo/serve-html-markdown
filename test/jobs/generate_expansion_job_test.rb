require "test_helper"

# Minitest 6 dropped Minitest::Mock, so `.stub` isn't available by default.
# Unlike the polyfill in test/services/claude_expand_service_test.rb, this one
# captures and restores the original method rather than removing it outright:
# ExpansionProcessor.process is defined directly on ExpansionProcessor's
# singleton class (no ancestor to fall back to), so a bare remove_method would
# delete it for the rest of the process and break every later test.
unless Object.method_defined?(:stub)
  class Object
    def stub(method_name, callable, &block)
      has_original = singleton_class.method_defined?(method_name) || singleton_class.private_method_defined?(method_name)
      original = singleton_class.instance_method(method_name) if has_original

      singleton_class.define_method(method_name) do |*args, &method_block|
        callable.call(*args, &method_block)
      end
      block.call
    ensure
      singleton_class.remove_method(method_name)
      singleton_class.define_method(method_name, original) if original
    end
  end
end

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

  test "stamps job_started after claiming the expansion" do
    with_processor(->(_) { "/notes--expand-1.html" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
    end

    assert_kind_of Integer, @expansion.reload.timings["job_started"]
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

  test "reports a truncated rewrite to the reader" do
    expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", question: "why?", mode: "edit_in_place"
    )
    ExpansionProcessor.stub(:process, ->(*) { raise ExpansionProcessor::TruncatedRewrite, "Rewrite looked truncated." }) do
      GenerateExpansionJob.perform_now(expansion.id)
    end

    assert_equal "failed", expansion.reload.status
    assert_equal "Rewrite looked truncated.", expansion.error_detail
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
