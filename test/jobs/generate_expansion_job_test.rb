require "test_helper"

class GenerateExpansionJobTest < ActiveJob::TestCase
  test "uses the test queue adapter" do
    assert_equal "test", ActiveJob::Base.queue_adapter_name
  end
end
